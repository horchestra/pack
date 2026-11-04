// Package tembo is a pack in the shape an author writes one: the application's own configuration, three methods.
package tembo

import (
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Config is tembo's configuration (github.com/ks-tool/tembo, docs/configuration.md): its settings are the
// environment variables TEMBO_* — the flag --pg-port is TEMBO_PG_PORT — and the file --config names. A node with no
// peers runs standalone: a cluster of one.
type Config struct {
	// ID is the node's id: lowercase letters, digits and underscores; the hostname, dashes turned into underscores,
	// when unset.
	ID string `json:"id,omitempty" env:"TEMBO_ID" jsonschema:"pattern=^[a-z0-9_]*$,maxLength=63"`
	// Listen and ListenRO are the client proxies: to the primary, and spread over the standbys.
	Listen   string `json:"listen,omitempty" env:"TEMBO_LISTEN"`
	ListenRO string `json:"listen_ro,omitempty" env:"TEMBO_LISTEN_RO"`
	// DB is the bbolt file of the node; PGData the Postgres data directory (required by the agent).
	DB     string `json:"db,omitempty" env:"TEMBO_DB"`
	PGData string `json:"pg_data,omitempty" env:"TEMBO_PG_DATA"`
	// PGPort is the port of Postgres itself, behind the proxies, the same on every node.
	PGPort int `json:"pg_port,omitempty" env:"TEMBO_PG_PORT" jsonschema:"minimum=1,maximum=65535"`
	// PGPassword is the superuser's password, the same on every node; it only bootstraps a new cluster, a running
	// one is rotated with `tembo rotate-password`.
	PGPassword sdk.Secret `json:"pg_password,omitempty" env:"TEMBO_PG_PASSWORD"`

	// Config is the file of the node's tags, below: loaded before the other sources, the environment wins over it.
	Config string `json:"config,omitempty" env:"TEMBO_CONFIG"`
	Node   Node   `json:"node,omitempty"`
}

// Node is the node's tags and the log it keeps.
type Node struct {
	// RaftRetentionLogSize is the bytes of applied raft log to keep; older entries are compacted into the snapshot.
	RaftRetentionLogSize int  `json:"raft_retention_log_size,omitempty"`
	NoFailover           bool `json:"nofailover,omitempty"`        // never the primary
	NoSync               bool `json:"nosync,omitempty"`            // never a synchronous standby
	FailoverPriority     int  `json:"failover_priority,omitempty"` // higher is preferred as a failover candidate
}

func (c *Config) SetDefaults() {
	c.Listen = ":5432"
	c.ListenRO = ":5434"
	c.DB = "/var/lib/postgresql/tembo.db"
	c.PGData = "/var/lib/postgresql/data"
	c.PGPort = 5433
	c.Config = "/etc/tembo/tembo.env"
	c.Node.RaftRetentionLogSize = 8 << 20 // 8 MiB, the agent's own default
}

func (*Config) Meta() sdk.Meta {
	return sdk.Meta{
		Name: "tembo", Version: "0.2.0", Description: "PostgreSQL with raft", License: "Apache-2.0",
		Source: "https://example.org/tembo",
		Image:  "images/tembo:0.2.0",
	}
}

func (*Config) PackRoot() packschema.Root {
	return packschema.Root{
		// the API listens on the port of the node's own peers address (8001 throughout the documentation); the
		// proxies are --listen and --listen-ro
		Ports:   map[string]int{"api": 8001, "pg": 5432, "pg-ro": 5434},
		Content: []packschema.File{{Path: "/etc/tembo/tembo.env", Template: "tembo.env.ptmpl", Values: "node", ParseAs: "text", Mode: "0640"}},
		Data:    map[string]packschema.Volume{"data": {MountPath: "/var/lib/postgresql", Size: "10Gi"}},
		Minimum: map[string]string{"memory": "1Gi"},
		// the image's entrypoint is `tembo`; the agent starts with `run`
		Command: []string{"tembo", "run"},
		// 200 only while the local Postgres answers SQL
		Readiness: &packschema.Readiness{HTTP: "api/health"},
	}
}

// PackSuggest offers the port next to the agent's own when a host of the target already listens on it.
func (c *Config) PackSuggest() map[string]packschema.Suggest {
	return map[string]packschema.Suggest{
		packschema.Pointer(c, &c.PGPort): {
			Expr: "has(facts.hosts) && facts.hosts.exists(h, has(facts.hosts[h].ports) && facts.hosts[h].ports.exists(p, p.port == 5433)) ? 5434 : null",
			Why:  "5433 is taken on a host",
		},
	}
}

// PackActions: a backup, which only reads, and a restore over the data, never run unasked.
func (*Config) PackActions() []packschema.Action {
	return []packschema.Action{{
		Name:    "backup",
		Command: []string{"tembo", "backup", "--to", "{{ .to }}", "--format", "{{ .format }}"},
		Timeout: "30m",
		Params: []packschema.Param{
			{Name: "to", Type: "string", Required: true},
			{Name: "format", Type: "string", Default: "custom", Enum: []any{"custom", "plain"}},
		},
	}, {
		Name:     "restore",
		Command:  []string{"tembo", "restore", "--from", "{{ .from }}"},
		Mutating: true,
		Approve:  true,
		Params:   []packschema.Param{{Name: "from", Type: "string", Required: true}},
	}}
}

// PackDiagnose: postgres that cannot listen because something on the host already does.
func (c *Config) PackDiagnose() []packschema.Diagnosis {
	return []packschema.Diagnosis{{
		ID:    "port-taken",
		Match: `address already in use.*:5433|could not bind .*5433`,
		Cause: "postgres cannot listen: something on the host already listens on 5433",
		Fix: []packschema.Fix{
			{Patch: packschema.Pointer(c, &c.PGPort), Expr: "5434", Why: "the port next to it"},
			{Command: "pack discover", Why: "see what listens on the hosts"},
		},
	}}
}
