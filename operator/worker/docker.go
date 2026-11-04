package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Docker is the docker (or podman) runtime (К6): the job runs in a container of the pack image — checkout and
// home mounted, the environment an env-file, the cache a volume of the worker's — labeled with the job and the
// request so a restarted worker finds it. A cancel is docker kill --signal INT.
type Docker struct {
	Bin   string // docker | podman; "docker" by default
	Image string // the pack image (A7)
	Log   func(line string)
}

var _ Runtime = &Docker{}

// The labels of К6.
const (
	labelJob     = "pack-operator.job"
	labelRequest = "pack-operator.request"
)

func (d *Docker) bin() string {
	if len(d.Bin) > 0 {
		return d.Bin
	}
	return "docker"
}

// Run starts the container and reads its stdout; a ctx done kills it with INT.
func (d *Docker) Run(ctx context.Context, run *Run) (int, error) {
	envPath, err := envFile(run.Dir, run.Env)
	if err != nil {
		return 1, err
	}
	name := "pack-operator-job-" + strings.TrimPrefix(run.Job.ID, "j-")
	args := []string{"run", "--rm", "--name", name, "-i",
		"--label", labelJob + "=" + run.Job.ID, "--label", labelRequest + "=" + run.Job.Request,
		"--env-file", envPath,
		"-v", run.Checkout + ":/checkout", "-v", run.Home + ":/home", "-v", run.Cache + ":/cache",
		"-e", "HOME=/home", "-e", "PACK_CACHE=/cache",
		"-w", "/checkout", d.Image}
	args = append(args, run.Job.Argv[1:]...) // the image's entrypoint is pack
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Cancel = func() error {
		return exec.Command(d.bin(), "kill", "--signal", "INT", name).Run()
	}
	cmd.WaitDelay = waitAfterInterrupt
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("the container: %w", err)
	}
	go d.logLines(stderr)
	events := bufio.NewScanner(stdout)
	events.Buffer(make([]byte, 1024*1024), 1024*1024)
	for events.Scan() {
		line := strings.TrimSpace(events.Text())
		if len(line) > 0 && line[0] == '{' {
			run.Event(json.RawMessage(line))
		}
	}
	err = cmd.Wait()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
		err = nil
	}
	return code, err
}

// Reattach finds the job's container by its label and reads on: the events up to lastSeq are skipped
// (the Bus holds them), the rest are read to the container's end (К6).
func (d *Docker) Reattach(ctx context.Context, run *Run, lastSeq int64) (int, error) {
	id, err := d.container(ctx, run.Job.ID)
	if err != nil {
		return 0, err
	}
	if len(id) == 0 {
		return 0, ErrLost
	}
	// read the log from the start, skip what the Bus holds, follow to the end
	cmd := exec.CommandContext(ctx, d.bin(), "logs", "-f", id)
	cmd.Cancel = func() error {
		return exec.Command(d.bin(), "kill", "--signal", "INT", id).Run()
	}
	cmd.WaitDelay = waitAfterInterrupt
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	var seen int64
	events := bufio.NewScanner(stdout)
	events.Buffer(make([]byte, 1024*1024), 1024*1024)
	for events.Scan() {
		line := strings.TrimSpace(events.Text())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		seen++
		if seen > lastSeq {
			run.Event(json.RawMessage(line))
		}
	}
	_ = cmd.Wait()
	out, err := exec.Command(d.bin(), "inspect", "-f", "{{.State.ExitCode}} {{.State.Running}}", id).Output()
	if err != nil {
		return 0, fmt.Errorf("the container's state: %w", err)
	}
	f := strings.Fields(string(out))
	code, _ := strconv.Atoi(f[0])
	if len(f) > 1 && f[1] == "true" {
		return 0, fmt.Errorf("the container %s is still running but the log ended", id)
	}
	_ = exec.Command(d.bin(), "rm", "-f", id).Run()
	return code, nil
}

// container is the id of the job's container, "" when there is none.
func (d *Docker) container(ctx context.Context, jobID string) (string, error) {
	out, err := exec.Command(d.bin(), "ps", "-a", "-q", "--filter", "label="+labelJob+"="+jobID).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// logLines reads the container's stderr into the worker's log.
func (d *Docker) logLines(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		if d.Log != nil {
			d.Log(sc.Text())
		}
	}
}
