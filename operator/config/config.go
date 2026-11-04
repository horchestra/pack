// Package config is the operator's configuration: one YAML file (К8). Projects may also come from the API
// (PUT /api/projects): the file is the source at start, the API writes the database.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/yaml"
)

// Config is the file whole.
type Config struct {
	Listen           string    `json:"listen"`             // :8080 by default
	DB               string    `json:"db"`                 // postgres://… or sqlite://<path>; the Bus is here too
	StateDSN         string    `json:"state_dsn"`          // the pack state store (К2), read only
	Auth             string    `json:"auth"`               // none; oidc is stage 3
	DevUser          string    `json:"dev_user"`           // the user of auth: none, admin in every domain
	Pack             string    `json:"pack"`               // the pack binary; PATH's by default
	DataDir          string    `json:"data_dir"`           // working copies and checkouts; the user cache's pack-operator by default
	Secrets          Secrets   `json:"secrets"`            // where git credentials and the projects' secrets live
	EncryptionKeyEnv string    `json:"encryption_key_env"` // encrypts the git credentials kept in the DB
	Git              Git       `json:"git"`
	Projects         []Project `json:"projects"`
}

// Secrets is the secret store of the operator's own secrets (git credentials first).
type Secrets struct {
	Provider string `json:"provider"` // openbao | file
	Addr     string `json:"addr"`
	Mount    string `json:"mount"`
	TokenEnv string `json:"token_env"`
}

// Git is how the operator commits (author) and fetches (fetch_interval).
type Git struct {
	Author        string   `json:"author"`
	FetchInterval Duration `json:"fetch_interval"`
}

// Project is one repository of packs and releases the operator runs.
type Project struct {
	ID          string            `json:"id"`
	Repo        string            `json:"repo"`
	Branch      string            `json:"branch"`
	Credentials string            `json:"credentials"` // a secret reference (secret:git/dev): token or SSH key (+ passphrase, known_hosts)
	Flow        string            `json:"flow"`        // direct | review
	Runtime     string            `json:"runtime"`     // native | docker | k8s
	Labels      map[string]string `json:"labels"`
	ApproveKeys []string          `json:"approve_keys"` // the public keys a Pack-Approve/Pack-Merge trailer must be signed with
	Gates       []Gate            `json:"gates"`
}

// Gate is a scenario gate: before an apply of release, the scenarios run on test_release (pack test --release).
type Gate struct {
	Release     string `json:"release"`
	TestRelease string `json:"test_release"`
}

// Duration is a time.Duration the YAML says as "30s".
type Duration struct{ time.Duration }

// UnmarshalJSON reads a duration string ("30s") or a number of nanoseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("a duration as \"30s\": %w", err)
		}
		d.Duration = v
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("a duration as \"30s\": %w", err)
	}
	d.Duration = time.Duration(n)
	return nil
}

// MarshalJSON writes the duration as "30s".
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// The defaults.
const (
	DefaultListen        = ":8080"
	DefaultDevUser       = "dev"
	DefaultFetchInterval = 30 * time.Second
)

// Load reads and checks the configuration of path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Listen) == 0 {
		c.Listen = DefaultListen
	}
	if len(c.DB) == 0 {
		return nil, fmt.Errorf("%s: db is required (postgres://… or sqlite://<path>)", path)
	}
	if len(c.Auth) == 0 {
		return nil, fmt.Errorf("%s: auth is required: none — only where nothing else is possible; oidc is stage 3", path)
	}
	if c.Auth != "none" {
		return nil, fmt.Errorf("%s: auth %s is not yet: none is the only mode until stage 3 (oidc)", path, c.Auth)
	}
	if len(c.DevUser) == 0 {
		c.DevUser = DefaultDevUser
	}
	if len(c.Pack) == 0 {
		c.Pack = "pack"
	}
	if len(c.DataDir) == 0 {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("data_dir is empty and the user cache is unknown: %w", err)
		}
		c.DataDir = filepath.Join(cache, "pack-operator")
	}
	if c.Git.FetchInterval.Duration == 0 {
		c.Git.FetchInterval = Duration{DefaultFetchInterval}
	}
	for i, p := range c.Projects {
		if len(p.ID) == 0 || len(p.Repo) == 0 {
			return nil, fmt.Errorf("%s: projects[%d]: id and repo are required", path, i)
		}
		if len(p.Branch) == 0 {
			c.Projects[i].Branch = "main"
		}
		switch p.Flow {
		case "", "direct", "review":
		default:
			return nil, fmt.Errorf("%s: project %s: flow is direct or review", path, p.ID)
		}
		switch p.Runtime {
		case "", "native", "docker", "k8s":
		default:
			return nil, fmt.Errorf("%s: project %s: runtime is native, docker or k8s", path, p.ID)
		}
	}
	return &c, nil
}
