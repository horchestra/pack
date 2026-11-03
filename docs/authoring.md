# Writing a pack

A pack is **an application's configuration as a type**, plus a few methods that say how the application runs. The
same pack can be written in Go (`sdk/`), Python (`sdk/python`) or Java (`sdk/java`); all three write the same
artifacts, byte for byte (`sdk/testdata/golden` is the proof the tests compare with).

| method          |                                                                                                                                         |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------|
| `SetDefaults()` | what a zero value cannot say. It sets, never adds: it may run twice. A field that is not zero afterwards is a setting with that default |
| `Meta()`        | the identity: `Name`, `Version` (required), `Description`, `License`, `Source`, `Image`, `Annotations` …                    |
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

| tag                  |                                                                                                                                                                                                                        |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `env:"NAME"`         | the field is the environment variable `NAME`                                                                                                                                                                           |
| `jsonschema:"readOnly=true"` | the operator does not set it: a value written for it is refused |

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

The expression sees `values` (the whole document), `pack.values.<key>` and `pack.keys.<key>` (another key's values
and address), `limits` and `me`. A field that is not there is an error at generation, not a broken schema; so is a name
an expression cannot select (a dash, a dot).

## Files

The files a component gets are the `Content` of `PackRoot`, not tags of the fields:

```go
Content: []packschema.File{
    {Path: "/etc/postgresql/postgresql.conf", Values: "conf", Format: "properties"}, // a subtree of the values
    {Path: "/opt/pack/init-extensions.sh", Template: "init-extensions.sh", Mode: "0755"}, // rendered
    {Path: "/opt/pack/restore-15.sh", Source: "restore-15.sh", Mode: "0755"},             // byte for byte
},
```

| from       |                                                                                                                                  |
|------------|----------------------------------------------------------------------------------------------------------------------------------|
| `Source`   | a file of `content/`, byte for byte, whatever `{{ }}` it holds: a script, a dump, an archive, a template of the application's own |
| `Template` | a file of `content/`, rendered, [emitters.md](emitters.md#template-data); with `Values`, written only when that value is there; `ParseAs` checks the result |
| `Values`   | a field's subtree (`conf`, `tls/key`) in `Format`, the name of `Path` saying it when there is none; `secret` mounts a password leaf's key |

`Mode` is the file's mode. The files themselves are in `content/`, next to the source (Go: the file that defines `Meta`;
Python: the class's file; Java: `Meta.dir`), read from disk, never embedded. Every file `Content` names must be there;
all of `content/` goes into the artifact as `templates/`.

## Installing from OS packages

A target that runs no image (`ansible`) installs what `Packages` of `PackRoot` says instead of `Image` and `Content`:

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

- **The key is any OS.** It is a string, not a field: nothing in pack or the SDK lists the OSes. The target takes the most
  exact one a host goes by (`debian-13`, `debian`, the family). The operator adds an OS the pack does not know, or other
  names, in `keys.<key>.packages` of `env.yaml`, without touching the pack.
- **Each OS has its own everything.** The same application reads another path and runs another unit there: the order is
  `Prepare` installed, `Repo` delivered, `Names` installed, `Commands` run, `Content` delivered, `Services` started.
- **The version is the OS's.** `Meta.Version` says nothing of a package; a name says its own in the OS's words
  (`postgresql-16=16.4-1.pgdg13+1`), or the name is a URL of a package file.
- **Where packages come from is a value.** A name and a file's `URL` are templates too: a field of the pack, upstream by
  default, lets the operator point at a mirror (`oci-proxy` of the ansible target serves a repository kept in the registry). `${os:codename}`, `${os:version}`, `${os:arch}`, `${os:id}` are the target's
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
  significant digits, at least 0.0001 — so that every SDK writes it back as it is); the others `x-pack-i18n: {ru: {title, description}}` beside them. `group` is `x-pack-group` on
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
held pack is installed **before** the pack that holds it. A pointer (Go), `Optional` (Python) or `null` (Java) makes it optional.
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
