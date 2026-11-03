// Package environment is where a pack is installed, as pack is told it: a
// file per environment and the Vault KV that file names — the same command,
// the same file, the same result, on a console or in CI. A pack described
// once (its values) goes to as many environments as there are files.
//
// The file, YAML or JSON, flattened by the app-config rule ({vault: {addr:
// …}} is vault_addr):
//
//	target: k8s                                # the system a pack is installed on
//	vault:
//	  addr: https://vault.example:8200         # VAULT_ADDR when the file has none
//	  paths: [secret/data/pack/prod]           # KV read over the file, the last path winning
//	namespace: data                            # and anything else the target wants
//	targets:                                   # more targets, by the name a key's platform.target gives
//	  edge:                                    # (a name has no underscore: the file is flattened)
//	    target: docker
//	    network: edge
//
//	store:                                     # where secrets are kept outside of any one target
//	  provider: openbao
//	  addr: https://bao.example:8200
//	  mount: secret
//	  prefix: pack/prod
//	  cacert: /etc/pack/ca.crt
//
// The store is a provider of its own, plugged in here, and none of the targets' business: its keys are not in a
// target's set. Its token comes from PACK_STORE_TOKEN, else VAULT_TOKEN, never from the file.
//
// The keys above are the default target: a key that says nothing of its target runs there. Each entry of targets
// is a target of its own — its own provider and its own settings, none inherited from the default one.
//
// The section `keys` — how the environment runs each key — is a tree, and Vault's KV is flat: from Vault (and from
// the overrides) it comes as one field `keys` holding the document as text, {key: {replicas: 3, …}}, merged over
// the file's entry by entry and field by field.
//
// The Vault token comes from VAULT_TOKEN only — it is never in the set.
// Everything else — the file's keys, Vault's over them, the command line's
// (--set) over both — is the set the provider gets, whole.
package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/ks-tool/app-config/go"

	"sigs.k8s.io/yaml"
)

// EnvVaultToken is where the Vault token comes from.
const EnvVaultToken = "VAULT_TOKEN"

// Environment is one environment: what pack itself reads of the set, and
// the set.
type Environment struct {
	// Target is the default target: a target's name, or the name of an entry of targets, which then is the default
	// one (Target its target, Settings its set).
	Target string `env:"TARGET"`
	Vault  Vault  `envPrefix:"VAULT_"`
	Store  Store  `envPrefix:"STORE_"`
	// Run and Secrets are pack's own sections: how it runs a saga, where it gets the passwords it creates. They are
	// not a target's, and no provider sees them.
	Run      Run      `envPrefix:"RUN_"`
	Secrets  Secrets  `envPrefix:"SECRETS_"`
	Discover Discover `envPrefix:"DISCOVER_"`
	// Lang is the language of what pack writes for the operator — the comments of values.yaml: one of the pack's
	// docs/ (ru, pt-BR); empty — $LANG's, else the pack's base language.
	Lang string `env:"LANG"`
	// Keys is how the environment runs each key of the pack, by the key's name (the section `keys`).
	Keys map[string]Key `env:"-"`
	// Settings is the whole set, keys in lower case: what the provider gets — the default target's, without the
	// entries of targets.
	Settings map[string]string `env:"-"`
	// Targets are the other targets, by name.
	Targets map[string]*Target `env:"-"`
}

// Target is one of the environment's other targets: what pack reads of it and the set its provider gets.
type Target struct {
	Target   string
	Settings map[string]string
}

// Store is the secret store of the environment (package secretstore): the section `store`.
type Store struct {
	Provider string `env:"PROVIDER"`
	Addr     string `env:"ADDR"`
	Mount    string `env:"MOUNT"`
	Prefix   string `env:"PREFIX"`
	CACert   string `env:"CACERT"`
}

// Run is how a saga runs.
type Run struct {
	Jobs       int  `env:"JOBS"`        // keys that run at once; 0 — as many as CPUs, 1 — one by one
	Strict     bool `env:"STRICT"`      // refuse a plan with an irreversible step
	NoRollback bool `env:"NO_ROLLBACK"` // leave what is in effect as it is on a failure
	// Keep are the keys a rollback after a failure leaves as they are.
	Keep []string `env:"KEEP"`
	// JournalKeep is how many sagas of an installation the journal keeps; an apply drops the older ones. 0 — the
	// default (DefaultJournalKeep), below 0 — all of them.
	JournalKeep int `env:"JOURNAL_KEEP"`
	// Approve is what an apply does unasked: auto-safe (the default) — a plan without a risk; plan — no plan; auto —
	// any plan. A plan it may not apply unasked needs --approve <its digest>.
	Approve string `env:"APPROVE"`
}

// The policies of run.approve.
const (
	ApproveAutoSafe = "auto-safe"
	ApprovePlan     = "plan"
	ApproveAuto     = "auto"
)

// ApprovePolicy is run.approve, the default when it says none; a word it does not know is an error.
func (r Run) ApprovePolicy() (string, error) {
	switch r.Approve {
	case "":
		return ApproveAutoSafe, nil
	case ApproveAutoSafe, ApprovePlan, ApproveAuto:
		return r.Approve, nil
	}
	return "", fmt.Errorf("run.approve: %q is not auto-safe, plan or auto", r.Approve)
}

// DefaultJournalKeep is the number of sagas the journal keeps when run.journal_keep says nothing.
const DefaultJournalKeep = 20

// Discover is what pack discover asks the targets of.
type Discover struct {
	Hosts []string `env:"HOSTS"` // the hosts to ask; none — every host the targets have
}

// Secrets is where the passwords a pack asks pack to create come from.
type Secrets struct {
	Provider  string   `env:"PROVIDER"`  // generate | env | file:<dir>
	Provision []string `env:"PROVISION"` // password leaves to provision, as values pointers
}

// Key is how the environment runs one key: where, how many, with what, and how it is reached. This — not the
// values — is where placement lives: values configure the application, the environment runs it.
type Key struct {
	Target      string            `json:"target,omitempty"`   // one of targets; "" — the default target
	Replicas    *int              `json:"replicas,omitempty"` // scale: replicas only
	Resources   *Resources        `json:"resources,omitempty"`
	Env         []EnvVar          `json:"env,omitempty"` // added to the component's own
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Reach       *Reach            `json:"reach,omitempty"` // how it is reached from outside its target
	// Raw is the rest of the entry: whatever the key's target has words for (hosts, publish, node selectors …).
	Raw map[string]any `json:"-"`
}

// Resources are cgroup requests and limits by resource name ("memory": "2Gi", "cpu": "500m").
type Resources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// EnvVar is a variable: a value, or a reference to a secret the operator keeps outside.
type EnvVar struct {
	Name      string     `json:"name"`
	Value     string     `json:"value,omitempty"`
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef names a secret and the key in it.
type SecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Reach is where a key can be reached from outside its target: the host of each instance and the port of each
// named port there.
type Reach struct {
	Instances []string       `json:"instances"`
	Ports     map[string]int `json:"ports,omitempty"`
}

type Vault struct {
	Addr  string   `env:"ADDR"`
	Paths []string `env:"PATHS"`
}

// Load reads the file (none — an empty one), then the Vault KV it names,
// then overrides, each over the one before.
func Load(ctx context.Context, file string, overrides map[string]string) (*Environment, error) {
	fileKV, err := readFile(file)
	if err != nil {
		return nil, err
	}
	over := lower(overrides)
	if err := noFlatKeys(over, "the overrides"); err != nil {
		return nil, err
	}
	env := &Environment{}
	if err := appconfig.Load(env, appconfig.KV(fileKV), appconfig.KV(over)); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var vaultKV map[string]string
	var keyTexts []string // the `keys` fields, in the order of the layers: each path of Vault, then the overrides
	if len(env.Vault.Paths) > 0 {
		addr := env.Vault.Addr
		if len(addr) == 0 {
			addr = os.Getenv("VAULT_ADDR")
		}
		if len(addr) == 0 {
			return nil, fmt.Errorf("%s: vault.paths without vault.addr (or VAULT_ADDR)", file)
		}
		token := os.Getenv(EnvVaultToken)
		if len(token) == 0 {
			return nil, fmt.Errorf("%s: vault.paths need %s", file, EnvVaultToken)
		}
		// path by path: the last path wins over the ones before it however a
		// key is spelled — one Read of all of them would keep namespace and
		// NAMESPACE apart, and lowering them after would pick one at random
		v := appconfig.NewVault(addr, appconfig.VaultToken{Token: token})
		vaultKV = map[string]string{}
		for _, path := range env.Vault.Paths {
			kv, err := v.Read(ctx, path)
			if err != nil {
				return nil, err
			}
			kv = lower(kv)
			if err := noFlatKeys(kv, "vault path "+path); err != nil {
				return nil, err
			}
			if text, ok := kv["keys"]; ok {
				keyTexts = append(keyTexts, text)
			}
			maps.Copy(vaultKV, kv)
		}
	}
	set := map[string]string{}
	maps.Copy(set, fileKV)
	maps.Copy(set, vaultKV)
	maps.Copy(set, over)
	env = &Environment{}
	if err := appconfig.Load(env, appconfig.KV(set)); err != nil {
		return nil, err
	}
	env.Settings = set
	tree, err := readKeyTree(file)
	if err != nil {
		return nil, err
	}
	// Vault and the overrides are flat: a nested document would lose where a key's name ends and a setting's
	// begins. They give the keys as one field `keys` holding the document as text (JSON or YAML); every layer's is
	// merged over what is there, in the order file, Vault paths, overrides — entry by entry and field by field.
	if text, ok := over["keys"]; ok {
		keyTexts = append(keyTexts, text)
	}
	for _, text := range keyTexts {
		extra, err := parseKeyTree(text)
		if err != nil {
			return nil, fmt.Errorf("%s: the keys field (Vault or an override): %w", file, err)
		}
		for key, entry := range extra {
			if tree[key] == nil {
				tree[key] = map[string]any{}
			}
			maps.Copy(tree[key], entry)
		}
	}
	if env.Keys, err = buildKeys(tree, file); err != nil {
		return nil, err
	}
	for k := range env.Settings {
		if k == "keys" {
			delete(env.Settings, k) // the keys' text, taken above
			continue
		}
		if k == "lang" {
			delete(env.Settings, k) // pack's: the language of what it writes for the operator
			continue
		}
		if ownSettings[k] || strings.HasPrefix(k, "keys_") {
			delete(env.Settings, k) // pack's or the store's, not a target's
		}
	}
	if err := splitTargets(file, env); err != nil {
		return nil, err
	}
	return env, nil
}

// ownSettings are the keys of the sections that are not a target's set: exactly the fields of Store, Run and
// Secrets. A key that only begins like one (run_as_user, registry_auth) is its target's.
var ownSettings = map[string]bool{
	"store_provider": true, "store_addr": true, "store_mount": true, "store_prefix": true, "store_cacert": true,
	"run_jobs": true, "run_strict": true, "run_no_rollback": true, "run_keep": true, "run_journal_keep": true, "run_approve": true,
	"secrets_provider": true, "secrets_provision": true,
	"discover_hosts": true,
}

const targetsPrefix = "targets_"

// splitTargets takes the entries of targets out of the default target's set: targets_<name>_<setting> is
// <setting> of target <name>.
func splitTargets(file string, env *Environment) error {
	for k, v := range env.Settings {
		rest, ok := strings.CutPrefix(k, targetsPrefix)
		if !ok {
			continue
		}
		delete(env.Settings, k)
		name, setting, ok := strings.Cut(rest, "_")
		if !ok || len(name) == 0 || len(setting) == 0 {
			return fmt.Errorf("%s: %s: a target is targets: {<name>: {target: …}}", file, k)
		}
		if env.Targets == nil {
			env.Targets = map[string]*Target{}
		}
		t := env.Targets[name]
		if t == nil {
			t = &Target{Settings: map[string]string{}}
			env.Targets[name] = t
		}
		t.Settings[setting] = v
	}
	for name, t := range env.Targets {
		t.Target = t.Settings["target"]
		if len(t.Target) == 0 {
			return fmt.Errorf("%s: targets.%s: say which target it is (target: docker, k8s …)", file, name)
		}
	}
	return defaultFromEntry(file, env)
}

// defaultFromEntry makes the entry of targets that `target:` names the default target. Its settings are all in the
// entry: one written beside it, at the top, is refused rather than dropped or mixed in.
func defaultFromEntry(file string, env *Environment) error {
	name := env.Target
	t, ok := env.Targets[name]
	if !ok {
		return nil
	}
	settings := maps.Clone(t.Settings)
	for k, v := range env.Settings {
		switch k {
		case "target":
		case "vault_addr", "vault_paths":
			settings[k] = v
		default:
			return fmt.Errorf("%s: %s: target: %s names an entry of targets, its settings are written there", file, k, name)
		}
	}
	env.Target, env.Settings = t.Target, settings
	delete(env.Targets, name)
	for key, k := range env.Keys {
		if k.Target == name {
			k.Target = ""
			env.Keys[key] = k
		}
	}
	return nil
}

// readFile flattens the environment file into keys in lower case.
func readFile(name string) (map[string]string, error) {
	if len(name) == 0 {
		return map[string]string{}, nil
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var dec appconfig.Decoder
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		dec = appconfig.DecodeTree(func(b []byte, v any) error { return yaml.Unmarshal(b, v) })
	case ".json":
		dec = appconfig.DecodeTree(json.Unmarshal)
	default:
		return nil, fmt.Errorf("%s: an environment is a .yaml or a .json file", name)
	}
	kv, err := dec(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return lower(kv), nil
}

// lower: one spelling per key, so a file's namespace and Vault's NAMESPACE
// are one setting; app-config matches them case-blind anyway.
func lower(kv map[string]string) map[string]string {
	out := make(map[string]string, len(kv))
	for k, v := range kv {
		out[strings.ToLower(k)] = v
	}
	return out
}

// noFlatKeys refuses keys_<name>_<setting> in a flat layer (a Vault path, the
// overrides): flattened, there is no saying where the key's name ends and the
// setting's begins — a flat layer gives the keys as one field `keys`, the
// document as text. The file's own keys_*, its `keys` section flattened, are
// not refused: the section is read as a tree.
func noFlatKeys(kv map[string]string, where string) error {
	for k := range kv {
		if strings.HasPrefix(k, "keys_") {
			return fmt.Errorf("%s: %s: the keys of a flat layer are one field `keys`, the document as text ({key: {replicas: 3, …}})", where, k)
		}
	}
	return nil
}

// readKeyTree reads the section `keys` of the environment file as a tree: the flattened set cannot say where a
// key's name ends and a setting's begins.
func readKeyTree(name string) (map[string]map[string]any, error) {
	tree := map[string]map[string]any{}
	if len(name) == 0 {
		return tree, nil
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Keys map[string]map[string]any `json:"keys"`
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(raw, &doc)
	default:
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: keys: %w", name, err)
	}
	for key, entry := range doc.Keys {
		if entry == nil {
			entry = map[string]any{}
		}
		tree[key] = entry
	}
	return tree, nil
}

// parseKeyTree reads the text of a `keys` field — {key: {…}} as JSON or YAML.
func parseKeyTree(text string) (map[string]map[string]any, error) {
	var tree map[string]map[string]any
	if err := yaml.Unmarshal([]byte(text), &tree); err != nil {
		return nil, err
	}
	return tree, nil
}

// buildKeys makes the keys of the tree: what the pack reads of an entry typed, the rest of it raw for the
// key's target. where names the source in an error.
func buildKeys(tree map[string]map[string]any, where string) (map[string]Key, error) {
	out := map[string]Key{}
	for key, entry := range tree {
		known := map[string]any{}
		rest := map[string]any{}
		for k, v := range entry {
			switch k {
			case "target", "replicas", "resources", "env", "labels", "annotations", "reach":
				known[k] = v
			default:
				rest[k] = v
			}
		}
		b, err := json.Marshal(known)
		if err != nil {
			return nil, err
		}
		var k Key
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&k); err != nil {
			return nil, fmt.Errorf("%s: keys.%s: %w", where, key, err)
		}
		if k.Reach != nil && len(k.Reach.Instances) == 0 {
			return nil, fmt.Errorf("%s: keys.%s.reach: instances say the host of each instance", where, key)
		}
		if len(rest) > 0 {
			k.Raw = rest
		}
		out[key] = k
	}
	return out, nil
}
