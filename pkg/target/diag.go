package target

import (
	"context"
	"errors"
)

// Logger is a target that gives the tail of what a key's instances wrote — their logs, the journal of their
// services — by instance: what pack diagnose looks at when a step of the key failed.
type Logger interface {
	Logs(ctx context.Context, key string, lines int) (map[string][]string, error)
}

// ErrNoLogs is a target that gives no logs.
var ErrNoLogs = errors.New("the target gives no logs")

// Health is how a key is on the target: ready when every instance is.
type Health struct {
	Ready     bool             `json:"ready"`
	Instances []InstanceHealth `json:"instances,omitempty"`
}

// InstanceHealth is how an instance is: its state in the target's own words (running, exited, active, Pending …)
// and, when it is not ready, why.
type InstanceHealth struct {
	Name   string `json:"name"`
	Ready  bool   `json:"ready"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Checker is a target that says how the keys it holds of the installation are, by key.
type Checker interface {
	Health(ctx context.Context) (map[string]Health, error)
}

// ErrNoHealth is a target that does not say how the keys are.
var ErrNoHealth = errors.New("the target does not say how the keys are")
