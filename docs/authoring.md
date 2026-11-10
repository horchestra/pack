# Writing a pack

A pack is **an application's configuration as a type**, plus a few methods that say how the application runs. The
same pack can be written in Go (`sdk/`), Python (`sdk/python`) or Java (`sdk/java`); all three write the same
artifacts, byte for byte (`sdk/internal/testdata/golden` is the proof the tests compare with). The Go SDK is a module of its own,
`github.com/horchestra/pack/sdk`: a pack depends on it alone, not on the tool.

| method          |                                                                                                                                         |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------|
| `SetDefaults()` | what a zero value cannot say. It sets, never adds: it may run twice. A field that is not zero afterwards is a setting with that default |
| `Meta()`        | the identity: `Name`, `Version` (required), `Description`, `License`, `Source`, `Image`, `Annotations` …                                |
| `PackRoot()`    | how it runs, upgrades and rolls back (below)                                                                                            |
| `PackCompute()` | optional: `{pointer: CEL formula}` that fills a value the operator did not write                                                        |
| `PackRules()`   | optional: `{pointer: [rule, …]}` that the values must satisfy                                                                           |

```go
type OpenBao struct {
Bao      Config        `json:"openbao"`
Postgres *tembo.Config `json:"postgres,omitempty"` // a pack of its own; a pointer makes it optional
}
```

## The struct

The fields are the application's own settings: the `json` tag is the key in the schema (use the application's own
name), **a field without `omitempty` is required**. Other tags:

| tag                                                      |                                                                                                                                                                                                                                               |
|----------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `env:"NAME"`                                             | the field is the environment variable `NAME`                                                                                                                                                                                                  |
| `jsonschema:"readOnly=true"`                             | the operator does not set it: a value written for it is refused                                                                                                                                                                               |
| `jsonschema:"enum=a,enum=b"`                             | the values it may be (a string, an integer, a number)                                                                                                                                                                                         |
| `jsonschema:"minimum=1,maximum=65535"`                   | the least and the most a number is                                                                                                                                                                                                            |
| `jsonschema:"pattern=^[a-z]+$,minLength=1,maxLength=63"` | what a string matches, its least and most characters; a comma in a value is `\,` — so a value cannot end with a backslash (it would swallow the next clause), and a tag in backquotes holds no backquote: `pack author scaffold` refuses both |

`sdk.Secret` is a credential leaf: the operator gives a reference (`secretRef: {name, key}`), never a value.

Replicas, resources and placement are **not** in the pack: they are the environment's (`keys:` of `env.yaml`, see
[release.md](release.md)). The values configure the application; the environment runs it.

## PackRoot

|                 |                                                                                                                                                                                                                                                                                                  |
|-----------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Ports`         | named ports; readiness and neighbours' addresses refer to a port by name                                                                                                                                                                                                                         |
| `Data`          | volumes that outlive an instance: `MountPath`, default `Size`                                                                                                                                                                                                                                    |
| `Minimum`       | the least it can run on (`cpu`, `memory`)                                                                                                                                                                                                                                                        |
| `Readiness`     | `TCP: "port"`, `HTTP: "port/path"` or `Exec`                                                                                                                                                                                                                                                     |
| `Command`       | replaces the image's entrypoint and cmd wholesale                                                                                                                                                                                                                                                |
| `Upgrade`       | `From`: a version constraint on what it can be upgraded from                                                                                                                                                                                                                                     |
| `Hooks`         | steps around an installation or a version change: `Name`, `Phase` (`preInstall`, `postInstall`, `preUpgrade`, `postUpgrade`, `preDowngrade`, `postDowngrade`), `Command`, `When{From,To}`, `Image` (`new`/`old`), `Compensate` (how to take the step back), `Exclusive`, `Timeout`, `Idempotent` |
| `RestartPolicy` | `Never`: a run-to-completion step (a bootstrap, a migration)                                                                                                                                                                                                                                     |
| `Savepoints`    | held packs after which a rollback stops, see [saga.md](saga.md)                                                                                                                                                                                                                                  |
| `Rollout`       | `MaxUnavailable`                                                                                                                                                                                                                                                                                 |

## Formulas and rules

A formula is CEL; it fills a leaf **only when the operator did not write it**. A rule is CEL that must hold, with a
message. Both are keyed by the field's JSON Pointer, and the names of fields inside an expression are the fields' own —
**the author writes none of them by hand**, so a renamed `json` name or a moved field moves along with them:

| Go                                      | Python                                   | Java                                             |                                       |
|-----------------------------------------|------------------------------------------|--------------------------------------------------|---------------------------------------|
| `packschema.Pointer(o, &o.Bao.Storage)` | `pointer(self, lambda o: o.bao.storage)` | `Paths.pointer(OpenBao.class, "bao", "storage")` | `/openbao/storage`, the map's key     |
| `packschema.Dotted(o, &o.Postgres)`     | `dotted(self, lambda o: o.postgres)`     | `Paths.dotted(OpenBao.class, "postgres")`        | `postgres`, as an expression reads it |

```go
func (o *OpenBao) PackCompute() map[string]string {
pg := packschema.Dotted(o, &o.Postgres)
return map[string]string{
packschema.Pointer(o, &o.Bao.Storage): fmt.Sprintf(
`has(pack.values.%[1]s) ? [{'type': 'postgresql', 'config': {'connection_url': 'postgres://' + pack.keys.%[1]s.service + ':5432/openbao'}}] : null`, pg),
}
}
```

A **suggestion** (`PackSuggest`, `pack_suggest`, `packSuggest`) is CEL too, keyed the same way, with why: a value
`pack fill` *offers* for a setting the operator did not give — from the facts of the key's target (`facts`, `pack
discover`) as often as not — and never takes by itself, as a formula does. `null` offers nothing:

```go
func (c *Config) PackSuggest() map[string]packschema.Suggest {
return map[string]packschema.Suggest{
packschema.Pointer(c, &c.PGPort): {
Expr: "has(facts.hosts) && facts.hosts.exists(h, has(facts.hosts[h].ports) && facts.hosts[h].ports.exists(p, p.port == 5433)) ? 5434 : null",
Why:  "5433 is taken on a host",
},
}
}
```

A suggestion on a password, or beside a formula, is a problem of `pack check`.

A **diagnosis** (`PackDiagnose`, `pack_diagnose`, `packDiagnose`) says what a failure of the pack looks like — `Match`,
RE2 over the failed step's error and the lines of its logs — what it is (`Cause`) and the ways out: a value for a
setting (`Patch`, its pointer, and `Expr`, CEL with the facts) or a command. `pack diagnose` tries the pack's before
the kinds every pack shares, and offers the fix:

```go
func (c *Config) PackDiagnose() []packschema.Diagnosis {
return []packschema.Diagnosis{{
ID:    "port-taken",
Match: `address already in use.*:5433`,
Cause: "postgres cannot listen: something on the host already listens on 5433",
Fix:   []packschema.Fix{{Patch: packschema.Pointer(c, &c.PGPort), Expr: "5434", Why: "the port next to it"}},
}}
}
```

The cause is said in other languages by `diagnose: {<id>: …}` of `docs/<lang>.yaml`.

An **action** (`PackActions`, `pack_actions`, `packActions`) is an operation the author names — a backup, a restore,
a rotation — that `pack run <name>` performs on a key of the pack: in the key's first instance as it runs —
`docker exec`, the
cluster's exec in the pod, the command on the key's first host — with its files, its data, its network and its
environment (the secrets the instance has, a password in its variables), into the journal: pack gives it no
credentials of its own. What it writes outlives it where the key keeps data (a backup under a volume's mount path). The
words of `Command` are
templates over the parameters (`{{ .to }}`); a parameter is a scalar (`string`, `integer`, `number`, `boolean`) with
a default and an enum if need be, written into the schema as the JSON Schema of an object (`params`), for a form to
be built on it. `Mutating` says it changes the installation — `run.approve: plan` asks for it — and `Approve` that it
is never run unasked but under `run.approve: auto` (a restore over the data). The actions are the closed list of what
a person or a model runs besides an apply:

```go
func (*Postgres) PackActions() []packschema.Action {
return []packschema.Action{{
Name:    "backup",
Command: []string{"pg_dump", "--format={{ .format }}", "--file={{ .to }}", "--dbname={{ .database }}"},
Params: []packschema.Param{
{Name: "to", Type: "string", Required: true},
{Name: "format", Type: "string", Default: "custom", Enum: []any{"custom", "plain", "directory"}},
{Name: "database", Type: "string", Default: "postgres"},
},
}, {
Name: "restore", Mutating: true, Approve: true,
Command: []string{"pg_restore", "--clean", "--if-exists", "--dbname={{ .database }}", "{{ .from }}"},
Params:  []packschema.Param{{Name: "from", Type: "string", Required: true}, {Name: "database", Type: "string", Default: "postgres"}},
}}
}
```

A word is one argument whatever its parameter holds: a command of `sh -c` puts the parameters into a shell, and
is the author's to quote. The descriptions of an action and of its parameters are `actions: {<name>: …,
<name>/<param>: …}` of `docs/<lang>.yaml`. A template that reads what is no parameter is a problem of `pack check`.

The expression sees `values` (the whole document), `pack.values.<key>` and `pack.keys.<key>` (another key's values
and address), `limits` and `me`. A field that is not there is an error at generation, not a broken schema; so is a name
an expression cannot select (a dash, a dot).

## Files

The files a component gets are the `Content` of `PackRoot`, not tags of the fields:

```go
Content: []packschema.File{
{Path: "/etc/postgresql/postgresql.conf", Values: "conf", Format: "properties"},      // a subtree of the values
{Path: "/opt/pack/init-extensions.sh", Template: "init-extensions.sh", Mode: "0755"}, // rendered
{Path: "/opt/pack/restore-15.sh", Source: "restore-15.sh", Mode: "0755"},             // byte for byte
},
```

| from       |                                                                                                                                                             |
|------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Source`   | a file of `content/`, byte for byte, whatever `{{ }}` it holds: a script, a dump, an archive, a template of the application's own                           |
| `Template` | a file of `content/`, rendered, [emitters.md](emitters.md#template-data); with `Values`, written only when that value is there; `ParseAs` checks the result |
| `Values`   | a field's subtree (`conf`, `tls/key`) in `Format`, the name of `Path` saying it when there is none; `secret` mounts a password leaf's key                   |

`Mode` is the file's mode. The files themselves are in `content/`, next to the source (Go: the file that defines `Meta`;
Python: the class's file; Java: `Meta.dir`), read from disk, never embedded. Every file `Content` names must be there;
all of `content/` goes into the artifact as `templates/`.

## Installing from OS packages

A target that runs no image (`host`, on hosts SSH reaches) installs what `Packages` of `PackRoot` says instead of `Image` and `Content`:

```go
Packages: map[string]packschema.OSPackages{
"debian": {
Prepare:  []string{"ca-certificates"},
Repo:     []packschema.File{
{Path: "/etc/apt/keyrings/docker.asc", URL: "{{ .mirror }}/debian/gpg", Mode: "0644"},
{Path: "/etc/apt/sources.list.d/docker.sources", Template: "docker.sources", Mode: "0644"},
},
Names:    []string{"docker-ce", "docker-ce-cli", "containerd.io"},
Content:  []packschema.File{{Path: "/etc/docker/daemon.json", Values: "daemon", Format: "json"}},
Services: []string{"docker"},
},
},
```

- **The key is any OS.** It is a string, not a field: nothing in pack or the SDK lists the OSes. The target takes the
  most
  exact one a host goes by (`debian-13`, `debian`, the family). The operator adds an OS the pack does not know, or other
  names, in `keys.<key>.packages` of `env.yaml`, without touching the pack.
- **Each OS has its own everything.** The same application reads another path and runs another unit there: the order is
  `Prepare` installed, `Sysctls` set and `Modules` loaded (kept in `/etc/sysctl.d` and `/etc/modules-load.d`, so a
  boot brings them back), `Repo` delivered, `Names` installed, `Commands` run, `Content` delivered, `Services` started.
- **The version is the OS's.** `Meta.Version` says nothing of a package; a name says its own in the OS's words
  (`postgresql-16=16.4-1.pgdg13+1`), or the name is a URL of a package file.
- **Where packages come from is a value.** A name and a file's `URL` are templates too: a field of the pack, upstream by
  default, lets the operator point at a mirror (`oci-proxy` of the host target serves a repository kept in the
  registry). `${os:codename}`, `${os:version}`, `${os:arch}`, `${os:id}` are the target's
  to resolve, on each host.
- **The hooks are `Hooks` of the root,** so a script a hook runs is at one path, with its own content per OS.
- **The host is taken to be empty:** nothing conflicting is looked for or removed.

## Documentation

What the operator reads about a setting — in a `values.yaml`, in a form built on the schema — is in `docs/<lang>.yaml`,
next to `content/`, one file a language, keyed by the field's JSON Pointer (`/` is the pack itself). The code says
nothing of it; the same files serve Go, Python and Java.

```yaml
# docs/en.yaml — the base language; base: true in another file names another
/:
  title: OpenBao
  description: Secrets management.
/openbao/log_level:
  title: Log level
  description: |
    How much the server says.
  examples:
    - debug
  group: logging
groups:
  logging: Logging
rules:
  storage-from-postgres: storage is filled from the embedded postgres and must not be set
diagnose:
  port-taken: "postgres cannot listen: the port is taken"
```

```yaml
# docs/ru.yaml — a translation: titles, descriptions, groups and rules
/openbao/log_level:
  title: Уровень журнала
groups:
  logging: Журнал
```

- The base language becomes the schema's own `title`, `description` and `examples` (typed by the field: `8080` of an
  integer is a number; a number is an integer of int64 or a plain decimal — no exponent, no trailing zero, at most 15
  significant digits, at least 0.0001 — so that every SDK writes it back as it is); the others
  `x-pack-i18n: {ru: {title, description}}` beside them. `group` is `x-pack-group` on
  the field, and the root lists the groups as `x-pack-groups` in the order the fields first use them. A rule's message
  is said by its `ID` (`Rule.ID`, unique in the pack), translated into the rule's `x-pack-i18n`.
- A pointer, a group or a rule id the pack has not is an error at generation: a renamed field leaves no stale text.
  A field of a held pack is documented in that pack's own `docs/`.
- The YAML is a subset the SDKs read without a library, and read the same: block mappings, lists of scalars, plain,
  `"double-"` or `'single-quoted'` scalars and `|` blocks; every scalar is a text. Flow `{…}`/`[…]`, anchors and `>` are
  refused.
- What is not said is a warning of `pack check` (and of the Go generator's `check`): a field with no description in the
  base language, a text with no translation, a rule with a message but no id. `--strict` makes them problems.
  `pack docs skeleton -l ru <dir>` writes the file a language starts from: every field, group and rule, with what the
  schema says in it and `""` where it says nothing, the base text as a comment above.

## A pack that holds a pack

A field whose type is a pack holds it: the settings of the two join in one schema (the held one as `pack://name#`), a
held pack is installed **before** the pack that holds it. A pointer (Go), `Optional` (Python) or `null` (Java) makes it
optional.
A pack that holds itself, however far down, is refused.

## Generating

|        |                                                                                                                              |
|--------|------------------------------------------------------------------------------------------------------------------------------|
| Go     | `sdk.Run(&OpenBao{})`: `generate [-o dir]`, `check`, `schema <name>`; `sdk.Generate(dir, roots…)`, `sdk.Verify(p)` in a test |
| Python | `packsdk.generate(dir, OpenBao())`; `packsdk.main(OpenBao())`: `generate`, `schema`                                          |
| Java   | `PackSdk.generate(Path, new OpenBao())`; `PackSdk.main(args, new OpenBao())`: `generate`, `schema`                           |

`generate` writes `<name>/schema.json`, `<name>/pack.yaml` and `<name>/templates/` for the pack and every pack it holds.
The author checks run on the result with `pack check <dir>`. Python and
Java: [../sdk/python/README.md](../sdk/python/README.md),
[../sdk/java/README.md](../sdk/java/README.md).

## Scaffolding from a spec

`pack author scaffold --spec spec.yaml --lang go|python|java -o dir` writes a pack's source from a short spec of it —
what a model gives instead of code, its output constrained by the spec's JSON Schema (`pack author scaffold
--schema`), or what a person writes by hand. The spec is the pack said as data: its name, version and image, its
settings as a tree of fields (a type of `string integer number boolean object list map secret`, required or not, an
environment variable, a default, a title and a description in each language, a group), `root` — `PackRoot` as the
schema's `x-pack` says it — and its formulas, rules, suggestions, diagnoses and actions by the fields' pointers:

```yaml
name: kv
version: "1.0.0"
image: images/kv:1.0.0
langs:
  - en
  - ru
fields:
  - name: password
    type: secret
    required: true
    env: KV_PASSWORD
    title: Password
    i18n:
      ru:
        title: Пароль
  - name: port
    type: integer
    default: 6380
    minimum: 1
    maximum: 65535
  - name: mode
    type: string
    default: standalone
    enum:
      - standalone
      - cluster
root:
  ports:
    kv: 6380
  readiness:
    tcp: kv
rules:
  /port:
    - id: port-high
      rule: values.port > 1024
      message: the port is above 1024
```

It writes the source as the examples are written (Go: the package and `cmd/main.go`, its import path the go.mod's
above the directory or `--go-import`; Python: one file; Java: one class), `docs/<lang>.yaml` of each language,
`content/` for the files the root names, and `tests/basic.yaml` — the required settings given, the least the pack
runs on as the environment's requests, an apply that installs. It is written once: a directory that holds any of
those files already is refused before one is written (`--force` writes over them), and from then on the source is
the pack, and `pack author check` is what says it is right. The spec itself is checked first — names, types, a
default of its field's type and inside its `enum`, `minimum`/`maximum`, `pattern`, `minLength`/`maxLength` (each of
the types that have it), a group of the spec's groups, the pointers of formulas and rules naming fields (not the
whole pack: a rule of the root is written in the source), the actions' params — so a model hears of a mistake there
before any code is written. `--from <generated pack>` takes the spec of a pack already
generated — the pack written again in another language, its `templates/` for `content/`; the examples go there and
back, byte for byte in Go and Java. A pack that holds another is not said by a spec yet: scaffold the held one by
itself.

## Checking while writing

`pack author check [src]` is the loop of writing a pack — a person's, or a model's writing it from a prompt: it
generates the pack from the source in the directory with the SDK of its language (`go run` of the package of
`sdk.Run`, `./cmd` when there is one; `python3` of the file of `packsdk.main`; `javac` and `java` of the class of
`PackSdk.main`, `--classpath` the SDK's classes), runs `pack check` and the docs checks on what it wrote and the
scenarios of `tests/` beside the source or above it, and says what to fix: each problem with the field's pointer and,
when the source writes the field on one line, its `file:line` (a json tag in Go, a field in Python, `json =` or the
camel field in Java); what the generator said when it failed (a compiler's line, the SDK's refusal). It says `ok`
when only warnings are left. `pack author cel <expr> --pack <generated> --key K` works an expression out as a formula
of the key would, on values (`--values`), facts (`--facts`, a fixture) and limits (`--env`); `pack author ref <topic>`
is the part of this guide a topic is, with the code of `--lang`; `pack author example postgres|openbao|docker` an
example's source; `pack author docs [src] -l ru` the `docs/<lang>.yaml` to fill. `pack mcp` has them as tools.

## Testing

`pack test <generated pack> [scenario.yaml|dir]…` runs the pack's scenarios — `tests/*.yaml` of the current
directory by default — each in a release of its own on the memory target, in the process: nothing is installed
anywhere, and the same scenarios hold whichever language wrote the pack. A scenario says what goes in and what comes
out; the paths in it are the scenario file's:

```yaml
name: postgres that cannot listen fails, is taken back, and is diagnosed a port conflict
facts: fixtures/hosts.json     # pack discover --save-fixture; or {<target>: {hosts: …}} inline
values:
  database: app
env: # env.yaml, over {target: memory}
  keys:
    postgres:
      resources:
        requests:
          memory: 1Gi
from: # a version installed first: the plan is from it
  pack: ../v15                 # another generated pack (a directory); none — the pack under test
  values:
    database: app
fail_at: postgres/wait         # <key>/<kind> (apply, wait, hook:<name> …): the step fails, its undoing does not
fail_error: postgres is not ready
logs: # what the target gives of the key's logs
  postgres:
    postgres-0:
      - 'could not bind IPv4 address "0.0.0.0": Address already in use'
expect:
  suggest: # what pack fill offers, by pointer; null — nothing
    /pg_port: 5434
  problems: # what the values break: where, kind, rule, a part of the message; [] — nothing
    - where: /openbao/storage
      rule: storage-from-postgres
  files: golden/basic          # the rendered files, as pack template -o writes them
  plan: golden/upgrade.plan    # the plan's text
  apply: # how the apply ends: installed or failed, the keys in effect after
    state: failed
    in_effect: [ ]
  diagnose: # what pack diagnose makes of it
    class: port_conflict
    remedies:
      - command: pack discover
```

Every scenario's installation has the same id, so that what is rendered of it — the names of the credentials — is
the same each run. `--update` (`UPDATE_GOLDEN=1`) writes `files` and `plan` from what the scenario gives. `--json`
says the results as `{scenarios: [{name, file, ok, failures}]}`; a scenario that fails ends pack test with
`test_failed`. `examples/postgres/tests` and `examples/openbao/tests` are scenarios of both kinds.

`pack test --release <dir> [scenario…]` runs the same scenarios on the target of a release instead — its pack, its
`targets:` and `registry:`, its `env.yaml` (the scenario's `env` over it, key by key of the top) and its lock, as `pack
refresh` left them — and installs for real: what a scenario installed is taken away after it (a saga left unfinished
undone, then the installation removed). `files` and `plan` are the memory target's, its addresses in them, and are
not compared there; a scenario with `fail_at` or `logs` is the memory target's and skipped — the result says what was
skipped. `tests/e2e` runs it on docker.
