// Package worker is the operator's executor (B6, К6): it takes the jobs of its runtime and label set off the
// Bus, runs them in a temporary job directory — checkout/ of the request's sha, home/ as the job's HOME —
// turns the NDJSON of --stream into job_event records and the final result into the job's state, and logs the
// stderr. PACK_CACHE is the worker's own directory: it outlives every job; checkout/ and home/ do not.
// A job asked to cancel gets the runtime's interrupt (SIGINT, docker kill, a Pod's deletion); a worker that
// restarts finds its docker and k8s jobs by their labels and reads on from last_seq — a native job is lost.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack/operator/bus"
	"github.com/horchestra/pack/operator/config"
	"github.com/horchestra/pack/operator/db"
	"github.com/horchestra/pack/operator/git"
)

// Runtime runs a job's argv in its job directory; the worker's three live below (native, docker, k8s).
type Runtime interface {
	// Run executes to the end, calling event with every NDJSON line of stdout in order; the exit code is the
	// result's. A ctx done interrupts the way the runtime interrupts (К6); Run still returns what happened.
	Run(ctx context.Context, run *Run) (exit int, err error)
	// Reattach finds a job that outlived the worker's restart by the runtime's own labels (К6), skips the
	// events up to lastSeq the Bus already holds, and reads on to the end — the same contract as Run.
	// ErrLost: the runtime keeps nothing of the job (a native job is always lost then).
	Reattach(ctx context.Context, run *Run, lastSeq int64) (exit int, err error)
}

// ErrLost is Reattach's answer when the runtime keeps nothing of the job.
var ErrLost = errors.New("the runtime keeps nothing of the job")

// Run is one execution: the job, the directories, the environment, the events sink.
type Run struct {
	Job      bus.Job
	Request  bus.Request
	RepoURL  string   // the project repository's URL (the k8s runtime's init container clones it)
	Dir      string   // the job's directory: checkout/ and home/ are inside
	Checkout string   // checkout/: the tree of the request's sha
	Home     string   // home/: the job's HOME
	Cache    string   // the worker's PACK_CACHE: it outlives the job
	Env      []string // the job's environment (К1: variables only, nothing in argv)
	Event    func(raw json.RawMessage)
	// Result is the stream's result event (К3: the job's result), when one came.
	Result json.RawMessage
}

// Worker is one executor: a runtime, a label set, a pack cache.
type Worker struct {
	cfg     *config.Config
	bus     *bus.SQLBus
	db      *db.DB
	creds   git.Store
	repos   *git.Repos
	runtime string
	labels  map[string]string
	id      string
	log     *zerolog.Logger
	rt      Runtime

	mu      sync.Mutex
	running map[string]context.CancelFunc
	repoURL string // a repository URL override (a runtime whose view of the repository differs — e.g. k8s)
}

// New is a worker for runtime ("native", "docker", "k8s") and the label set it answers; repos gives the
// checkouts (the projects' repositories).
func New(cfg *config.Config, b *bus.SQLBus, d *db.DB, creds git.Store, repos *git.Repos,
	runtime string, labels map[string]string, rt Runtime) *Worker {
	return &Worker{cfg: cfg, bus: b, db: d, creds: creds, repos: repos, runtime: runtime, labels: labels,
		id: "worker-" + runtime + "-" + shortLabels(labels), log: logging.For("worker"), rt: rt,
		running: map[string]context.CancelFunc{}}
}

// ID is the worker's name (the Bus group it takes jobs from).
func (w *Worker) ID() string { return w.id }

// Config is the worker's configuration (a second worker over the same ground is built from it).
func (w *Worker) Config() *config.Config { return w.cfg }

// Repos is the worker's working copies (a second worker shares them).
func (w *Worker) Repos() *git.Repos { return w.repos }

// RunningJobs counts the jobs the worker holds right now (a shutdown waits for zero).
func (w *Worker) RunningJobs() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.running)
}

// SetRepoURL overrides the repository URL the runtimes see (a k8s runtime reaches the repository over the
// network, not the operator's own file:// path).
func (w *Worker) SetRepoURL(url string) { w.repoURL = url }

// group is the Bus group of the worker's runtime and label set: every job belongs to exactly one.
func group(runtime string, labels map[string]string) string {
	return "worker-" + runtime + "-" + shortLabels(labels)
}

// shortLabels is a label set in one short string (deterministic).
func shortLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "any"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s;", k, labels[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// matches says whether the job is this worker's: same runtime, same label set exactly.
func (w *Worker) matches(job bus.Job) bool {
	rt := job.Runtime
	if len(rt) == 0 {
		rt = "native"
	}
	return rt == w.runtime && reflect.DeepEqual(job.Labels, w.labels)
}

// Run takes and runs jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.recover(ctx); err != nil {
		w.log.Warn().Err(err).Msg("the recovery")
	}
	sub, err := w.bus.Subscribe(ctx, group(w.runtime, w.labels), 0)
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		m, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if m.Kind == bus.KindJob {
			var job bus.Job
			if err := m.Decode(&job); err == nil && w.matches(job) && job.State == bus.JobQueued {
				w.take(ctx, job)
			}
		}
		_ = sub.Ack(ctx, m.Seq)
	}
}

// recover finds the jobs the worker's previous life left in starting or running (К6): a docker or k8s job is
// found by the runtime's labels and read on from its last_seq; a native job is lost.
func (w *Worker) recover(ctx context.Context) error {
	jobs, err := w.bus.JobsByState(ctx, bus.JobStarting, bus.JobRunning)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if !w.matches(job) {
			continue
		}
		req, err := w.bus.GetRequest(ctx, job.Request)
		if err != nil {
			continue
		}
		seq := job.LastSeq
		run := &Run{Job: job, Request: req, Cache: w.cacheDir(w.project(req.Repo))}
		run.Event = w.eventSink(ctx, job.ID, &seq, run)
		w.log.Info().Str("job", job.ID).Int64("from_seq", seq).Msg("reattaching")
		exit, err := w.rt.Reattach(ctx, run, seq)
		if errors.Is(err, ErrLost) {
			w.log.Warn().Str("job", job.ID).Msg("lost at the worker's restart")
			w.finish(ctx, job, bus.JobLost, nil, 0)
			continue
		}
		switch {
		case err != nil || exit != 0:
			w.finish(ctx, job, bus.JobFailed, run.Result, exit)
		default:
			w.finish(ctx, job, bus.JobSucceeded, run.Result, exit)
		}
	}
	return nil
}

// eventSink is the job's job_event publisher: numbered from the seq the Bus already holds, the job record's
// last_seq moved with every event (a restart dedupes by it).
func (w *Worker) eventSink(ctx context.Context, jobID string, seq *int64, run *Run) func(json.RawMessage) {
	log := w.log.With().Str("job", jobID).Logger()
	return func(raw json.RawMessage) {
		var ev struct {
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		if ev.Type == "result" {
			run.Result = ev.Result
		}
		*seq++
		if _, err := w.bus.Publish(ctx, bus.KindJobEvent, bus.JobEvent{Kind: bus.KindJobEvent, Job: jobID, Seq: *seq, Event: raw}); err != nil {
			log.Warn().Err(err).Msg("an event is not published")
			return
		}
		_ = w.bus.SetJobLastSeq(ctx, jobID, *seq)
	}
}

// take claims a queued job and runs it in the background.
func (w *Worker) take(ctx context.Context, job bus.Job) {
	cur, err := w.bus.GetJob(ctx, job.ID)
	if err != nil || cur.State != bus.JobQueued {
		return // another worker was first
	}
	cur.State = bus.JobStarting
	cur.Worker = w.id
	cur.Started = time.Now().UTC()
	if err := w.bus.PublishJob(ctx, cur); err != nil {
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.running[cur.ID] = cancel
	w.mu.Unlock()
	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.running, cur.ID)
			w.mu.Unlock()
			cancel()
		}()
		w.execute(runCtx, cur, cancel)
	}()
}

// execute is one job's life: the directory, the environment, the events, the state. The context ends only
// when the job is asked to cancel (never at the worker's own shutdown: docker and k8s jobs outlive it).
func (w *Worker) execute(ctx context.Context, job bus.Job, cancel context.CancelFunc) {
	log := w.log.With().Str("job", job.ID).Str("request", job.Request).Logger()
	req, err := w.bus.GetRequest(ctx, job.Request)
	if err != nil {
		w.finish(ctx, job, bus.JobFailed, nil, 1)
		return
	}
	proj := w.project(req.Repo)
	if proj == nil {
		w.finish(ctx, job, bus.JobFailed, nil, 1)
		return
	}
	dir, err := w.jobDir(ctx, proj, req)
	if err != nil {
		log.Warn().Err(err).Msg("the job directory")
		w.finish(ctx, job, bus.JobFailed, nil, 1)
		return
	}
	defer os.RemoveAll(dir) // checkout/ and home/ do not outlive the job (К6); the cache is the worker's
	env, err := w.jobEnv(proj, req, dir)
	if err != nil {
		log.Warn().Err(err).Msg("the job's environment")
		w.finish(ctx, job, bus.JobFailed, nil, 1)
		return
	}
	job.State = bus.JobRunning
	if err := w.bus.PublishJob(ctx, job); err != nil {
		return
	}
	seq := job.LastSeq
	repoURL := proj.Repo
	if len(w.repoURL) > 0 {
		repoURL = w.repoURL
	}
	run := &Run{Job: job, Request: req, RepoURL: repoURL, Dir: dir, Checkout: filepath.Join(dir, "checkout"),
		Home: filepath.Join(dir, "home"), Cache: filepath.Join(w.cacheDir(proj)), Env: env}
	run.Event = w.eventSink(ctx, job.ID, &seq, run)
	// the heartbeat: the job says it lives; an outside cancel (the record's state) interrupts the run
	pulse, stopPulse := context.WithCancel(context.Background())
	defer stopPulse()
	go w.heartbeat(pulse, job.ID, cancel)

	exit, runErr := w.rt.Run(ctx, run)
	switch {
	case ctx.Err() != nil:
		w.finish(ctx, job, bus.JobCanceled, run.Result, exit) // an outside cancel interrupted the run
	case exit != 0 || runErr != nil:
		if runErr != nil {
			log.Warn().Err(runErr).Msg("the run")
		}
		w.finish(ctx, job, bus.JobFailed, run.Result, exit)
	default:
		w.finish(ctx, job, bus.JobSucceeded, run.Result, exit)
	}
}

// heartbeat keeps the job's record fresh and watches for an outside cancel: the record's state turning
// canceled (the API's answer to POST /api/jobs/{id}/cancel) interrupts the run.
func (w *Worker) heartbeat(ctx context.Context, jobID string, cancel context.CancelFunc) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		cur, err := w.bus.GetJob(ctx, jobID)
		if err != nil {
			continue
		}
		if cur.State == bus.JobCanceled {
			cancel()
			return
		}
		if cur.State != bus.JobRunning {
			continue
		}
		// the heartbeat touches the record without a state change: watchers see the worker is alive
		if err := w.bus.PublishJob(ctx, cur); err != nil {
			continue
		}
	}
}

// finish writes the job's last state and result.
func (w *Worker) finish(ctx context.Context, job bus.Job, state string, result json.RawMessage, exit int) {
	cur, err := w.bus.GetJob(ctx, job.ID)
	if err == nil {
		job = cur
	}
	job.State = state
	job.Result = result
	job.Exit = exit
	job.Ended = time.Now().UTC()
	if err := w.bus.PublishJob(context.Background(), job); err != nil {
		w.log.Error().Err(err).Str("job", job.ID).Msg("the job's last state is not recorded")
	}
}

// project is the config's project of id.
func (w *Worker) project(id string) *config.Project {
	for i := range w.cfg.Projects {
		if w.cfg.Projects[i].ID == id {
			return &w.cfg.Projects[i]
		}
	}
	return nil
}

// cacheDir is the worker's PACK_CACHE of a project.
func (w *Worker) cacheDir(proj *config.Project) string {
	return filepath.Join(w.cfg.DataDir, "cache", proj.ID)
}
