// Package policy is the operator's policy (B5, §3.2): it turns requests into jobs and approvals into the jobs
// they unblock. An apply is always through a plan: a plan job on the request's sha, then — when the release's
// run.approve allows (auto, or auto-safe and the plan has no risks) — an apply job of the plan's digest, else
// the request waits for an approval of that same sha and digest (the API, or a signed trailer through sync).
// A rollback, a delete, a mutating run go through a dry-run and an approval the same way. A gate of the project
// (pack test --release on the gate's test release) stands between the plan and the apply. One mutating job on
// a release at a time: what comes while one runs waits its turn. A new commit stales what waits for approval.
package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/authz"
	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
)

// The kinds of jobs the policy makes (the how of operator_jobs): plan → gate → apply is the pipeline of a plan
// or apply request; dryrun → the approved job is the one of a rollback, a delete, a mutating run; plain is a
// request that asks for no change (refresh, discover, test).
const (
	howPlan   = "plan"
	howGate   = "gate"
	howDryRun = "dryrun"
	howApply  = "apply"
	howPlain  = "plain"
)

// Git gives the policy a tree of a project at a sha — the release's env.yaml is read there (run.approve).
type Git interface {
	Checkout(ctx context.Context, project, sha, dest string) error
}

// Policy is the engine; Run drives it off the Bus.
type Policy struct {
	cfg *config.Config
	bus *bus.SQLBus
	db  *db.DB
	az  *authz.Authz
	git Git
	log *zerolog.Logger
}

// New is the policy; git may be nil where no decision reads the release's env (run.approve is auto-safe then).
func New(cfg *config.Config, b *bus.SQLBus, d *db.DB, az *authz.Authz, git Git) *Policy {
	return &Policy{cfg: cfg, bus: b, db: d, az: az, git: git, log: logging.For("policy")}
}

// SetGit points the policy at the trees it reads run.approve from (the synchronizer, once it exists).
func (p *Policy) SetGit(g Git) { p.git = g }

// Run consumes the Bus until ctx ends: requests, the states of jobs, approvals. Only the leader of "policy"
// works; the projections (operator_requests, operator_jobs) are rebuilt from the events it confirms.
func (p *Policy) Run(ctx context.Context) error {
	sub, err := p.bus.Subscribe(ctx, "policy", 0)
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
		if ok, err := p.bus.Leader(ctx, "policy", time.Minute); err != nil || !ok {
			if err != nil {
				p.log.Warn().Err(err).Msg("the leadership")
			}
			_ = sub.Ack(ctx, m.Seq)
			continue
		}
		if err := p.handle(ctx, m); err != nil {
			p.log.Error().Err(err).Int64("seq", m.Seq).Str("kind", m.Kind).Msg("the event is not handled")
		}
		_ = sub.Ack(ctx, m.Seq)
	}
}

func (p *Policy) handle(ctx context.Context, m bus.Message) error {
	switch m.Kind {
	case bus.KindRequest:
		var req bus.Request
		if err := m.Decode(&req); err != nil {
			return err
		}
		return p.onRequest(ctx, req)
	case bus.KindJob:
		var job bus.Job
		if err := m.Decode(&job); err != nil {
			return err
		}
		return p.onJob(ctx, job)
	case bus.KindApproval:
		var a bus.Approval
		if err := m.Decode(&a); err != nil {
			return err
		}
		return p.onApproval(ctx, a)
	}
	return nil
}

// ---- requests

// onRequest records a request and, when it is new (open), starts it — or queues it behind the release's
// mutating job. A plan request of a new sha stales the release's requests that wait for an approval.
func (p *Policy) onRequest(ctx context.Context, req bus.Request) error {
	if err := p.upsertRequest(ctx, req); err != nil {
		return err
	}
	if req.State != bus.RequestOpen {
		return nil
	}
	if req.Action == bus.ActionPlan || req.Action == bus.ActionApply {
		if err := p.staleWaiting(ctx, req); err != nil {
			return err
		}
	}
	return p.start(ctx, req)
}

// start makes the request's first job — unless the release has a mutating job in flight, then the request
// waits its turn (open, picked up when the job ends). A request that already has a job is never started again
// (its own updates come back as events).
func (p *Policy) start(ctx context.Context, req bus.Request) error {
	has, err := p.hasJobs(ctx, req.ID)
	if err != nil || has {
		return err
	}
	mutating := mutates(req)
	if mutating {
		busy, err := p.releaseBusy(ctx, req.Repo, req.Target.Release)
		if err != nil {
			return err
		}
		if busy {
			p.log.Info().Str("request", req.ID).Str("release", req.Target.Release).Msg("queued: the release is busy")
			return nil
		}
	}
	proj := p.project(req.Repo)
	runtime, labels := "native", map[string]string(nil)
	if proj != nil {
		runtime, labels = proj.Runtime, proj.Labels
		if len(runtime) == 0 {
			runtime = "native"
		}
	}
	argv := func(args ...string) []string {
		a := []string{"pack", "-C", req.Target.Release}
		if len(req.Params.Env) > 0 {
			a = append(a, "--env", req.Params.Env)
		}
		return append(a, args...)
	}
	switch req.Action {
	case bus.ActionPlan, bus.ActionApply:
		_, err = p.queue(ctx, req, howPlan, true, argv("plan", "--stream"), runtime, labels)
	case bus.ActionRollback:
		args := []string{"rollback"}
		if req.Params.To > 0 {
			args = append(args, "--to", fmt.Sprint(req.Params.To))
		}
		_, err = p.queue(ctx, req, howDryRun, true, argv(append(args, "--dry-run", "--stream")...), runtime, labels)
	case bus.ActionDelete:
		_, err = p.queue(ctx, req, howDryRun, true, argv("delete", "--dry-run", "--stream"), runtime, labels)
	case bus.ActionRun:
		// a run's mutating is the action's, known only to the pack: the dry-run path covers both (B7 may
		// refine); the dry-run says the digest, the approval runs it
		args := []string{"run", req.Params.Run}
		for k, v := range req.Params.RunParams {
			args = append(args, "--param", k+"="+v)
		}
		_, err = p.queue(ctx, req, howDryRun, true, argv(append(args, "--dry-run", "--stream")...), runtime, labels)
	case bus.ActionRefresh:
		_, err = p.queue(ctx, req, howPlain, false, argv("refresh", "--stream"), runtime, labels)
	case bus.ActionDiscover:
		_, err = p.queue(ctx, req, howPlain, false, argv("discover", "--stream"), runtime, labels)
	case bus.ActionTest:
		_, err = p.queue(ctx, req, howPlain, false, []string{"pack", "test", "--release", req.Target.Release, "--stream"}, runtime, labels)
	default:
		return fmt.Errorf("request %s: the action %s", req.ID, req.Action)
	}
	return err
}

// mutates says whether the request's pipeline changes the release — those are the jobs the queue serializes.
func mutates(req bus.Request) bool {
	switch req.Action {
	case bus.ActionPlan, bus.ActionApply, bus.ActionRollback, bus.ActionDelete, bus.ActionRun:
		return true
	}
	return false
}

// queue makes a job of the request and queues it.
func (p *Policy) queue(ctx context.Context, req bus.Request, how string, mutating bool, argv []string, runtime string, labels map[string]string) (bus.Job, error) {
	job, err := bus.NewJob(req, argv, runtime, labels)
	if err != nil {
		return bus.Job{}, err
	}
	if err := p.insertJob(ctx, job, how, mutating, req.Repo, req.Target.Release); err != nil {
		return bus.Job{}, err
	}
	if err := p.bus.PublishJob(ctx, job); err != nil {
		return bus.Job{}, err
	}
	p.log.Info().Str("job", job.ID).Str("request", req.ID).Str("how", how).Msg("queued")
	return job, nil
}

// ---- jobs

// onJob records a job's state; a terminal one moves its request on and frees the release's queue.
func (p *Policy) onJob(ctx context.Context, job bus.Job) error {
	terminal := job.State == bus.JobSucceeded || job.State == bus.JobFailed ||
		job.State == bus.JobCanceled || job.State == bus.JobLost
	row, err := p.jobRow(ctx, job.ID)
	if err != nil {
		// a job the policy never recorded (a recovery's): record it, and only terminal ones move requests
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if terminal {
			p.log.Warn().Str("job", job.ID).Msg("a job of no record ended")
		}
		return nil
	}
	if err := p.updateJob(ctx, job); err != nil {
		return err
	}
	if !terminal {
		return nil
	}
	req, err := p.request(ctx, row.request)
	if err != nil {
		return err
	}
	if job.State == bus.JobLost {
		p.log.Warn().Str("job", job.ID).Str("request", req.ID).Msg("a job is lost: the request fails, no blind retry")
		return p.finishRequest(ctx, req, bus.RequestFailed)
	}
	if job.State == bus.JobFailed || job.State == bus.JobCanceled {
		return p.finishRequest(ctx, req, bus.RequestFailed)
	}
	switch row.how {
	case howPlan:
		return p.planDone(ctx, req, job)
	case howGate:
		// the scenarios passed: the decision the plan left is next
		return p.decide(ctx, req)
	case howDryRun:
		var result struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(job.Result, &result); err != nil || len(result.Digest) == 0 {
			return p.finishRequest(ctx, req, bus.RequestFailed)
		}
		return p.waitApproval(ctx, req, result.Digest)
	case howApply, howPlain:
		return p.finishRequest(ctx, req, bus.RequestDone)
	}
	return nil
}

// planDone reads the plan job's result: the digest and the risks. A gate stands first when the project has one
// for the release; else the run.approve decision.
func (p *Policy) planDone(ctx context.Context, req bus.Request, job bus.Job) error {
	var plan struct {
		Digest string   `json:"digest"`
		Risks  []string `json:"risks"`
	}
	if err := json.Unmarshal(job.Result, &plan); err != nil || len(plan.Digest) == 0 {
		return p.finishRequest(ctx, req, bus.RequestFailed)
	}
	if err := p.setDigest(ctx, req.ID, plan.Digest); err != nil {
		return err
	}
	if gate := p.gate(req.Repo, req.Target.Release); gate != nil {
		proj := p.project(req.Repo)
		runtime, labels := proj.Runtime, proj.Labels
		_, err := p.queue(ctx, req, howGate, true,
			[]string{"pack", "test", "--release", gate.TestRelease, "--stream"}, runtime, labels)
		return err
	}
	return p.decide(ctx, req)
}

// decide is the run.approve of the release: auto applies any plan, auto-safe a plan without risks, anything
// else waits for an approval of the plan's sha and digest.
func (p *Policy) decide(ctx context.Context, req bus.Request) error {
	digest, err := p.digest(ctx, req.ID)
	if err != nil {
		return err
	}
	mode := p.approveMode(ctx, req)
	risks, err := p.risks(ctx, req.ID)
	if err != nil {
		return err
	}
	if mode == "auto" || (mode == "auto-safe" && len(risks) == 0) {
		return p.apply(ctx, req, digest)
	}
	return p.waitApproval(ctx, req, digest)
}

// apply queues the apply job of an approved digest.
func (p *Policy) apply(ctx context.Context, req bus.Request, digest string) error {
	proj := p.project(req.Repo)
	runtime, labels := "native", map[string]string(nil)
	if proj != nil {
		runtime, labels = proj.Runtime, proj.Labels
	}
	argv := []string{"pack", "-C", req.Target.Release}
	if len(req.Params.Env) > 0 {
		argv = append(argv, "--env", req.Params.Env)
	}
	argv = append(argv, "apply", "--approve", digest, "--stream")
	_, err := p.queue(ctx, req, howApply, true, argv, runtime, labels)
	return err
}

// approved queues the job an approval unblocks: the apply, or the approved rollback/delete/run.
func (p *Policy) approved(ctx context.Context, req bus.Request, digest string) error {
	proj := p.project(req.Repo)
	runtime, labels := "native", map[string]string(nil)
	if proj != nil {
		runtime, labels = proj.Runtime, proj.Labels
	}
	argv := []string{"pack", "-C", req.Target.Release}
	if len(req.Params.Env) > 0 {
		argv = append(argv, "--env", req.Params.Env)
	}
	switch req.Action {
	case bus.ActionPlan, bus.ActionApply:
		argv = append(argv, "apply", "--approve", digest, "--stream")
	case bus.ActionRollback:
		argv = append(argv, "rollback", "--approve", digest, "--stream")
	case bus.ActionDelete:
		argv = append(argv, "delete", "--approve", digest, "--stream")
	case bus.ActionRun:
		argv = append(argv, "run", req.Params.Run, "--approve", digest)
		for k, v := range req.Params.RunParams {
			argv = append(argv, "--param", k+"="+v)
		}
		argv = append(argv, "--stream")
	default:
		return fmt.Errorf("request %s: an approval of a %s", req.ID, req.Action)
	}
	_, err := p.queue(ctx, req, howApply, true, argv, runtime, labels)
	return err
}

// ---- approvals

// Approve is the trailer path of К5 (sync's Policy): a signed commit approved a release's plan at a sha and
// digest. The signature was checked against the project's keys already; here the sha and digest must match a
// request that waits.
func (p *Policy) Approve(ctx context.Context, project, release, digest, sha, by string) error {
	req, err := p.waiting(ctx, project, release, sha, digest)
	if err != nil {
		return fmt.Errorf("the trailer of %s: %w", sha, err)
	}
	_, err = p.bus.Publish(ctx, bus.KindApproval, bus.Approval{
		Kind: bus.KindApproval, Request: req.ID, Digest: digest, SHA: sha, By: by, Via: "trailer", At: time.Now().UTC()})
	return err
}

// Merge is the change-request path of К5: B9 owns it.
func (p *Policy) Merge(ctx context.Context, project, change, sha, by string) error {
	p.log.Info().Str("project", project).Str("change", change).Str("sha", sha).Msg("a merge trailer: B9's, not yet")
	return nil
}

// ApproveAPI is the API path (К4 POST /api/requests/{id}/approve): the right approve on the release (casbin)
// and the sha and digest of the request that waits.
func (p *Policy) ApproveAPI(ctx context.Context, user, requestID, digest, sha string) error {
	req, err := p.request(ctx, requestID)
	if err != nil {
		return err
	}
	ok, err := p.az.Enforce(user, req.Repo, "release:"+req.Target.Release, "approve")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s may not approve %s", errForbidden, user, req.Target.Release)
	}
	if req.State != bus.RequestWaitingApproval || req.SHA != sha {
		return fmt.Errorf("%w: no request %s waits for an approval at %s", errConflict, requestID, sha)
	}
	want, err := p.digest(ctx, req.ID)
	if err != nil {
		return err
	}
	if want != digest {
		return fmt.Errorf("%w: the digest is %s, not %s", errConflict, want, digest)
	}
	_, err = p.bus.Publish(ctx, bus.KindApproval, bus.Approval{
		Kind: bus.KindApproval, Request: req.ID, Digest: digest, SHA: sha, By: user, Via: "api", At: time.Now().UTC()})
	return err
}

var (
	errForbidden = errors.New("forbidden")
	errConflict  = errors.New("conflict")
)

// onApproval unblocks the request an approval names.
func (p *Policy) onApproval(ctx context.Context, a bus.Approval) error {
	req, err := p.request(ctx, a.Request)
	if err != nil {
		return err
	}
	if req.State != bus.RequestWaitingApproval || req.SHA != a.SHA {
		p.log.Warn().Str("request", a.Request).Str("by", a.By).Msg("an approval nothing waits for — ignored")
		return nil
	}
	want, err := p.digest(ctx, req.ID)
	if err != nil {
		return err
	}
	if want != a.Digest {
		p.log.Warn().Str("request", a.Request).Str("digest", a.Digest).Msg("an approval of another digest — ignored")
		return nil
	}
	if err := p.setState(ctx, req, bus.RequestOpen); err != nil {
		return err
	}
	return p.approved(ctx, req, a.Digest)
}

// ---- the release's env

// approveMode is the release's run.approve at the request's sha: auto | auto-safe (the default) | plan.
func (p *Policy) approveMode(ctx context.Context, req bus.Request) string {
	if p.git == nil {
		return "auto-safe"
	}
	dest, err := os.MkdirTemp("", "pack-operator-policy-")
	if err != nil {
		return "auto-safe"
	}
	defer os.RemoveAll(dest)
	if err := p.git.Checkout(ctx, req.Repo, req.SHA, dest); err != nil {
		p.log.Warn().Err(err).Str("sha", req.SHA).Msg("no checkout for run.approve: auto-safe")
		return "auto-safe"
	}
	name := "env.yaml"
	if len(req.Params.Env) > 0 {
		name = "env." + req.Params.Env + ".yaml"
	}
	b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(req.Target.Release), name))
	if err != nil {
		return "auto-safe"
	}
	var doc struct {
		Run struct {
			Approve string `json:"approve"`
		} `json:"run"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "auto-safe"
	}
	switch doc.Run.Approve {
	case "auto", "auto-safe", "plan":
		return doc.Run.Approve
	}
	return "auto-safe"
}

// gate is the project's scenario gate of the release, nil when there is none.
func (p *Policy) gate(repo, release string) *config.Gate {
	if proj := p.project(repo); proj != nil {
		for i := range proj.Gates {
			if proj.Gates[i].Release == release {
				return &proj.Gates[i]
			}
		}
	}
	return nil
}

func (p *Policy) project(id string) *config.Project {
	for i := range p.cfg.Projects {
		if p.cfg.Projects[i].ID == id {
			return &p.cfg.Projects[i]
		}
	}
	return nil
}

// ---- the projections (operator_requests, operator_jobs, migration 0005)

func (p *Policy) upsertRequest(ctx context.Context, req bus.Request) error {
	_, err := p.db.ExecContext(ctx, p.bind(`insert into operator_requests (id, repo, release, sha, action, digest, state, body, updated_at)
		values (?, ?, ?, ?, ?, '', ?, ?, current_timestamp)
		on conflict (id) do update set state = excluded.state, body = excluded.body, updated_at = excluded.updated_at`),
		req.ID, req.Repo, req.Target.Release, req.SHA, req.Action, req.State, mustJSON(req))
	return err
}

// setState moves a request and publishes it.
func (p *Policy) setState(ctx context.Context, req bus.Request, state string) error {
	req.State = state
	if err := p.bus.UpdateRequest(ctx, req); err != nil {
		return err
	}
	return nil
}

// finishRequest ends a request — done or failed — and starts what queued behind it.
func (p *Policy) finishRequest(ctx context.Context, req bus.Request, state string) error {
	if err := p.setState(ctx, req, state); err != nil {
		return err
	}
	return p.startNext(ctx, req.Repo, req.Target.Release)
}

// waitApproval parks the request on the digest.
func (p *Policy) waitApproval(ctx context.Context, req bus.Request, digest string) error {
	if err := p.setDigest(ctx, req.ID, digest); err != nil {
		return err
	}
	p.log.Info().Str("request", req.ID).Str("digest", digest).Msg("waiting for an approval")
	return p.setState(ctx, req, bus.RequestWaitingApproval)
}

// startNext starts the release's oldest open request, when the release is free.
func (p *Policy) startNext(ctx context.Context, repo, release string) error {
	busy, err := p.releaseBusy(ctx, repo, release)
	if err != nil || busy {
		return err
	}
	var id string
	err = p.db.QueryRowContext(ctx, p.bind(`select id from operator_requests
		where repo = ? and release = ? and state = 'open' order by updated_at, id limit 1`), repo, release).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	req, err := p.request(ctx, id)
	if err != nil {
		return err
	}
	return p.start(ctx, req)
}

// staleWaiting moves the release's requests waiting for an approval to stale: a newer commit is there.
func (p *Policy) staleWaiting(ctx context.Context, newer bus.Request) error {
	rows, err := p.db.QueryContext(ctx, p.bind(`select id from operator_requests
		where repo = ? and release = ? and state = 'waiting_approval' and sha <> ?`),
		newer.Repo, newer.Target.Release, newer.SHA)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		req, err := p.request(ctx, id)
		if err != nil {
			return err
		}
		p.log.Info().Str("request", id).Str("sha", req.SHA).Msg("stale: a newer commit is there")
		if err := p.setState(ctx, req, bus.RequestStale); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *Policy) hasJobs(ctx context.Context, requestID string) (bool, error) {
	var n int
	err := p.db.QueryRowContext(ctx, p.bind(`select count(*) from operator_jobs where request = ?`), requestID).Scan(&n)
	return n > 0, err
}

// releaseBusy says whether the release has a mutating job in flight (queued, starting, running).
func (p *Policy) releaseBusy(ctx context.Context, repo, release string) (bool, error) {
	var n int
	err := p.db.QueryRowContext(ctx, p.bind(`select count(*) from operator_jobs
		where repo = ? and release = ? and mutating = 1 and state in ('queued', 'starting', 'running')`),
		repo, release).Scan(&n)
	return n > 0, err
}

func (p *Policy) request(ctx context.Context, id string) (bus.Request, error) {
	var body []byte
	if err := p.db.QueryRowContext(ctx, p.bind(`select body from operator_requests where id = ?`), id).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return bus.Request{}, fmt.Errorf("no request %s", id)
		}
		return bus.Request{}, err
	}
	var req bus.Request
	return req, json.Unmarshal(body, &req)
}

// waiting finds the request of the release waiting for an approval of sha and digest.
func (p *Policy) waiting(ctx context.Context, repo, release, sha, digest string) (bus.Request, error) {
	var id string
	err := p.db.QueryRowContext(ctx, p.bind(`select id from operator_requests
		where repo = ? and release = ? and sha = ? and digest = ? and state = 'waiting_approval'
		order by updated_at desc limit 1`), repo, release, sha, digest).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return bus.Request{}, fmt.Errorf("no request of %s waits for an approval of %s at %s", release, digest, sha)
	}
	if err != nil {
		return bus.Request{}, err
	}
	return p.request(ctx, id)
}

func (p *Policy) setDigest(ctx context.Context, id, digest string) error {
	_, err := p.db.ExecContext(ctx, p.bind(`update operator_requests set digest = ?, updated_at = current_timestamp where id = ?`),
		digest, id)
	return err
}

func (p *Policy) digest(ctx context.Context, id string) (string, error) {
	var digest string
	err := p.db.QueryRowContext(ctx, p.bind(`select digest from operator_requests where id = ?`), id).Scan(&digest)
	return digest, err
}

// risks is what the plan job of the request found, kept on its job row.
func (p *Policy) risks(ctx context.Context, requestID string) ([]string, error) {
	var raw []byte
	err := p.db.QueryRowContext(ctx, p.bind(`select j.body from operator_jobs j
		where j.request = ? and j.how = 'plan' order by j.updated_at desc limit 1`), requestID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var job bus.Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	var plan struct {
		Risks []string `json:"risks"`
	}
	if err := json.Unmarshal(job.Result, &plan); err != nil {
		return nil, nil
	}
	return plan.Risks, nil
}

type jobRow struct {
	request string
	how     string
}

func (p *Policy) jobRow(ctx context.Context, id string) (jobRow, error) {
	var row jobRow
	err := p.db.QueryRowContext(ctx, p.bind(`select request, how from operator_jobs where id = ?`), id).
		Scan(&row.request, &row.how)
	return row, err
}

func (p *Policy) insertJob(ctx context.Context, job bus.Job, how string, mutating bool, repo, release string) error {
	m := 0
	if mutating {
		m = 1
	}
	_, err := p.db.ExecContext(ctx, p.bind(`insert into operator_jobs (id, request, repo, release, how, mutating, state, body, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, current_timestamp)`),
		job.ID, job.Request, repo, release, how, m, job.State, mustJSON(job))
	return err
}

func (p *Policy) updateJob(ctx context.Context, job bus.Job) error {
	_, err := p.db.ExecContext(ctx, p.bind(`update operator_jobs set state = ?, body = ?, updated_at = current_timestamp where id = ?`),
		job.State, mustJSON(job), job.ID)
	return err
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// bind numbers the ? of a query for PostgreSQL ($1, $2 …); SQLite takes them as they are.
func (p *Policy) bind(query string) string {
	if p.db.SQLite() {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
