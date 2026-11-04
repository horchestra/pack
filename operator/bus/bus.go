// Package bus is the operator's Bus (К3): an event log behind an interface — publish, subscribe from a position,
// confirm the processing, elect a leader — so that the parts (sync, policy, workers, API) speak through records,
// not through calls. The SQL implementation lives here (PostgreSQL and SQLite, the operator's own database);
// the interface is what a NATS or Kafka implementation would take over.
//
// A group processes its events in order: a subscription's Next claims the next unclaimed event; Ack confirms
// everything up to a seq — an event claimed but never confirmed is claimed again after the claim's lease, never
// lost, never given twice at once.
package bus

import (
	"context"
	"encoding/json"
	"time"
)

// Message is one published event: its position, its kind (a record of К3's), the record itself as JSON.
type Message struct {
	Seq  int64
	Kind string
	Body json.RawMessage
	At   time.Time
}

// Decode reads the message's record.
func (m Message) Decode(v any) error { return json.Unmarshal(m.Body, v) }

// Bus is the interface: publish, subscribe, lead.
type Bus interface {
	// Publish appends an event of kind (a record of К3's as any) and returns its position.
	Publish(ctx context.Context, kind string, body any) (int64, error)
	// Submit makes a request known, once: the same request again (the same key, К3) returns the one that
	// stands — created is false — and publishes nothing.
	Submit(ctx context.Context, req Request) (Request, bool, error)
	// Subscribe is the group's position: events after it. The group keeps its position between subscriptions
	// (the database holds it); from says where a new group starts.
	Subscribe(ctx context.Context, group string, from int64) (Subscription, error)
	// Leader tries to be — or to stay, called again within the ttl — the leader of name.
	Leader(ctx context.Context, name string, ttl time.Duration) (bool, error)
}

// Subscription is a group's claim on the events after its position.
type Subscription interface {
	// Next waits for the next event and claims it for this subscriber; the same event is never claimed by
	// another subscriber of the group at the same time.
	Next(ctx context.Context) (Message, error)
	// Ack confirms the processing of everything up to seq: the group's position moves past it.
	Ack(ctx context.Context, seq int64) error
	// Close ends the subscription; its unconfirmed claims expire with their lease.
	Close() error
}

// The kinds of К3.
const (
	KindRequest  = "request"
	KindApproval = "approval"
	KindJob      = "job"
	KindJobEvent = "job_event"
)
