// Package secretstore is where the secrets of an installation are kept outside of any one target: the values
// pack generates when the operator gave none, the values a job publishes, and the ones the operator refers to by
// a path of the store. A store is a provider the environment plugs in:
//
//	store:
//	  provider: openbao            # a kind registered here; openbao and vault speak KV v2
//	  addr: https://bao.example:8200
//	  mount: secret                # the KV v2 mount (default secret)
//	  prefix: pack/prod            # where pack keeps its own (default pack/<installation>)
//	  cacert: /etc/pack/ca.crt     # to verify the server
//
// The token never is in the file: PACK_STORE_TOKEN, else VAULT_TOKEN. A pack runs without a store; then nothing
// here is used, and a target keeps what it is given the way it always did.
package secretstore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Store keeps secrets: a secret is a path and the fields under it.
type Store interface {
	// Get returns the fields of the secret at path; ok is false when there is none.
	Get(ctx context.Context, path string) (fields map[string]string, ok bool, err error)
	// Put writes the secret at path: all of its fields; what was there is replaced.
	Put(ctx context.Context, path string, fields map[string]string) error
}

// Config is the store's section of the environment.
type Config struct {
	Provider string
	Addr     string
	Mount    string
	Prefix   string
	CACert   string
	// Token is not in the file; Open takes it from the environment when it is empty.
	Token string
}

// Env* are where the token comes from.
const (
	EnvToken         = "PACK_STORE_TOKEN"
	EnvVaultToken    = "VAULT_TOKEN"
	defaultMount     = "secret"
	defaultPrefixDir = "pack"
)

// Factory opens a store of one kind.
type Factory func(Config) (Store, error)

var (
	mu        sync.Mutex
	factories = map[string]Factory{}
)

// Register makes a kind of store available to the environment's `provider`.
func Register(kind string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	factories[kind] = f
}

// Kinds are the registered kinds, sorted.
func Kinds() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Configured: the environment names a store.
func (c Config) Configured() bool { return len(c.Provider) > 0 }

// Open the store the config names; nil, nil when it names none.
func Open(c Config) (Store, error) {
	if !c.Configured() {
		return nil, nil
	}
	mu.Lock()
	f, ok := factories[c.Provider]
	mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("store.provider %q: the kinds are %s", c.Provider, strings.Join(Kinds(), ", "))
	}
	if len(c.Token) == 0 {
		for _, name := range []string{EnvToken, EnvVaultToken} {
			if c.Token = os.Getenv(name); len(c.Token) > 0 {
				break
			}
		}
	}
	return f(c)
}

// SettingKeys are the keys of a target's set that carry the store: the target is told, whole, where secrets are
// kept — pack never writes them into the provider's environment or arguments.
var SettingKeys = []string{"store_provider", "store_addr", "store_mount", "store_prefix", "store_cacert", "store_token"}

// Settings is the store as keys of a target's set. The token is taken from the environment here, once: a
// provider is another process and reads none of ours.
func (c Config) Settings() map[string]string {
	if !c.Configured() {
		return nil
	}
	out := map[string]string{"store_provider": c.Provider, "store_addr": c.Addr, "store_mount": c.Mount, "store_prefix": c.Prefix,
		"store_cacert": c.CACert, "store_token": c.Token}
	if len(out["store_token"]) == 0 {
		for _, name := range []string{EnvToken, EnvVaultToken} {
			if out["store_token"] = os.Getenv(name); len(out["store_token"]) > 0 {
				break
			}
		}
	}
	for k, v := range out {
		if len(v) == 0 {
			delete(out, k)
		}
	}
	return out
}

// FromSettings is the store a target's set names; the zero Config when it names none.
func FromSettings(set func(key string) string) Config {
	return Config{Provider: set("store_provider"), Addr: set("store_addr"), Mount: set("store_mount"),
		Prefix: set("store_prefix"), CACert: set("store_cacert"), Token: set("store_token")}
}

// Prefix is where pack keeps its own secrets of an installation: store.prefix, else pack/<installation>.
func (c Config) PrefixFor(installation string) string {
	if len(c.Prefix) > 0 {
		return strings.Trim(c.Prefix, "/")
	}
	return defaultPrefixDir + "/" + installation
}

// PathOf is where a named secret of the installation lives: <prefix>/<name>.
func (c Config) PathOf(installation, name string) string {
	return c.PrefixFor(installation) + "/" + strings.Trim(name, "/")
}

// Fields returns f's keys sorted — for messages.
func Fields(f map[string]string) []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
