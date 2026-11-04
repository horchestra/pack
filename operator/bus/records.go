package bus

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// The actions a request asks for (К3).
const (
	ActionPlan     = "plan"
	ActionApply    = "apply"
	ActionRollback = "rollback"
	ActionDelete   = "delete"
	ActionRun      = "run"
	ActionRefresh  = "refresh"
	ActionDiscover = "discover"
	ActionTest     = "test"
)

// The states of a request (К3).
const (
	RequestOpen            = "open"
	RequestWaitingApproval = "waiting_approval"
	RequestDone            = "done"
	RequestFailed          = "failed"
	RequestCanceled        = "canceled"
	RequestStale           = "stale"
)

// The states of a job (К3).
const (
	JobQueued    = "queued"
	JobStarting  = "starting"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
	JobLost      = "lost"
)

// Target is what a request is about: a release, or a pack (an author's project, for pack-ui).
type Target struct {
	Release string `json:"release,omitempty"`
	Pack    string `json:"pack,omitempty"`
}

// Params are a request's parameters (К3); the zero value is the plain action.
type Params struct {
	Env       string            `json:"env"`
	Digest    string            `json:"digest"`
	To        int               `json:"to"`
	Key       string            `json:"key"`
	Run       string            `json:"run"`
	RunParams map[string]string `json:"run_params"`
	DryRun    bool              `json:"dry_run"`
}

// Request is a request record (К3): who asked what on which inputs.
type Request struct {
	Kind   string    `json:"kind"`
	ID     string    `json:"id"`
	Key    string    `json:"key"`
	Repo   string    `json:"repo"`
	SHA    string    `json:"sha"`
	Target Target    `json:"target"`
	Action string    `json:"action"`
	Params Params    `json:"params"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	State  string    `json:"state"`
}

// Approval is an approval record (К3): a digest on a sha, by someone, through the API or a commit trailer.
type Approval struct {
	Kind    string    `json:"kind"`
	Request string    `json:"request"`
	Digest  string    `json:"digest"`
	SHA     string    `json:"sha"`
	By      string    `json:"by"`
	Via     string    `json:"via"` // api | trailer
	At      time.Time `json:"at"`
}

// Job is a job record (К3): one argv of pack in a runtime, taken by a worker by labels.
type Job struct {
	Kind    string            `json:"kind"`
	ID      string            `json:"id"`
	Request string            `json:"request"`
	Argv    []string          `json:"argv"`
	Runtime string            `json:"runtime"`
	Labels  map[string]string `json:"labels"`
	State   string            `json:"state"`
	Worker  string            `json:"worker"`
	Saga    string            `json:"saga"`
	LastSeq int64             `json:"last_seq"`
	Result  json.RawMessage   `json:"result"`
	Exit    int               `json:"exit"`
	Created time.Time         `json:"created"`
	Started time.Time         `json:"started"`
	Ended   time.Time         `json:"ended"`
}

// JobEvent is one event a running job gave (К3): the stream of pack, numbered by the job.
type JobEvent struct {
	Kind  string          `json:"kind"`
	Job   string          `json:"job"`
	Seq   int64           `json:"seq"`
	Event json.RawMessage `json:"event"` // a pkg/api.Event, as it came
}

// RequestKey is the idempotence key of a request: sha256 of the canonical JSON of what identifies it —
// repo, sha, target, action, params (К3). The same work asked twice is one key.
func RequestKey(repo, sha string, target Target, action string, params Params) string {
	canonical := struct {
		Repo   string `json:"repo"`
		SHA    string `json:"sha"`
		Target Target `json:"target"`
		Action string `json:"action"`
		Params Params `json:"params"`
	}{repo, sha, target, action, params}
	b, err := json.Marshal(canonical) // a struct in field order, a map in key order: canonical
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NewRequest is a request ready to submit: id and key minted, state open.
func NewRequest(repo, sha string, target Target, action string, params Params, by string) (Request, error) {
	id, err := mint("r")
	if err != nil {
		return Request{}, err
	}
	return Request{
		Kind:   KindRequest,
		ID:     id,
		Key:    RequestKey(repo, sha, target, action, params),
		Repo:   repo,
		SHA:    sha,
		Target: target,
		Action: action,
		Params: params,
		By:     by,
		At:     time.Now().UTC(),
		State:  RequestOpen,
	}, nil
}

// mint is an id: a prefix and 12 bytes of randomness, hex.
func mint(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}
