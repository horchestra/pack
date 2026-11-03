# pack

A package format and the tool that installs it, independent of any platform. Besides the core, the repository holds two
leaf packages that import nothing themselves: `emit` (templates, config formats) and `secretstore` (the secret store
clients).

The reference is in [docs/](docs/README.md). The targets (docker, ansible, k8s) are programs of their own, in
[github.com/horchestra/targets](https://github.com/horchestra/targets).

## The model

- **A pack is an application's configuration as a Go struct.** Methods say the rest: `SetDefaults()`, `Meta()`
  (name, version, image, OCI annotations), `PackRoot()` (ports, data volumes, readiness, upgrade hooks, savepoints),
  optionally `PackCompute()` (CEL formulas that fill a value) and `PackRules()` (CEL checks).
- **A pack may hold another pack** in a field; a pointer makes it optional. The packs' settings join in one JSON
  schema, and the order of installation follows the containment: a held pack is installed before the pack that
  holds it.
- **Files** are the `Content` of `PackRoot`: a file of `content/` as it is (`Source`) or rendered (`Template`), or a
  subtree of the values in a format (`Values`, `Format`); `content/` is read from disk next to the pack's source, never
  embedded. A field tagged `env:"NAME"` is an environment variable, one without `omitempty` is required.
- **`sdk`** is the author's side (Go; Python and Java in `sdk/python`, `sdk/java` write the same artifacts):
  `sdk.Run(&Root{})` is a cobra command that generates the pack's directory (`schema.json`, `pack.yaml`, `templates/`)
  and checks it.

## A release

The directory a command runs in (`-C` names another), the way terraform runs in a root module:

| file                                  | what                                                                                                                                                                                                      |
|---------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `release.yaml`                        | `pack:` a directory or `oci://host/repo:tag`; `values:` more files; `state: file` (default, `.pack/state.json`) or `target`; `id:` the installation's UUID (`state: target` only — `pack refresh` writes it) |
| `values.yaml`, `values/`              | what the operator wants, in the pack's own words                                                                                                                                                          |
| `env.yaml` (`--env x` → `env.x.yaml`) | `target`, `registry`, `run` (`jobs`, `strict`, `no_rollback`, `keep`, `journal_keep`), `secrets`, `store`, `keys` (where and how each key runs: replicas, resources, env)                                 |
| `release.lock.json`                   | the digest a registry tag meant when `pack init` or `pack refresh` ran; commit it                                                                                                                                           |
| `.pack/`                              | state, journal, the packs brought from a registry (`packs/<digest>/`)                                                                                                                                     |

The command line adds nothing to a release.

## Formulas and rules

`PackCompute` fills a value by a CEL formula, `PackRules` checks the values. Both are keyed by the field's JSON Pointer,
and the names of fields inside a formula are the field's too: the author writes none of them by hand, so a renamed
json tag or a moved field moves them along (a name that is not there is a panic at generation, not a broken schema).

- `packschema.Pointer(o, &o.Bao.Storage)` — `/openbao/storage`, the key of the map;
- `packschema.Dotted(o, &o.Postgres)` — `postgres`, the way a formula reads it: `values.openbao.storage`,
  `pack.values.postgres`, `pack.keys.postgres.service`.

```go
func (o *OpenBao) PackCompute() map[string]string {
  pg := packschema.Dotted(o, &o.Postgres)
  return map[string]string{
	  packschema.Pointer(o, &o.Bao.Storage): fmt.Sprintf(
		  `has(pack.values.%[1]s) ? [{'type': 'postgresql', 'config': {'connection_url': 'postgres://' + pack.keys.%[1]s.service + ':5432/openbao'}}] : null`, pg),
  }
}

func (o *OpenBao) PackRules() map[string][]packschema.Rule {
  pg, storage := packschema.Dotted(o, &o.Postgres), packschema.Dotted(o, &o.Bao.Storage)
  return map[string][]packschema.Rule{
	  packschema.Pointer(o, &o.Bao.Storage): {{
		  Rule:    fmt.Sprintf("!has(pack.values.%s) || !has(values.%s) || size(values.%[2]s) == 0", pg, storage), 
		  Message: "storage is filled from the embedded postgres and must not be set",
    }},
  }
}
```

The same goes for the names in `PackRoot` (a savepoint is a key: `packschema.Dotted(o, &o.Postgres)`). What stays a
string is what is not a field: the literals of the formula (`'postgres://'`, the database's name).

## Commands

|                                     |                                                                                                      |
|-------------------------------------|------------------------------------------------------------------------------------------------------|
| `init <oci://…\|dir> [dir]`         | make a release: `release.yaml`, `env.yaml`, a documented `values.yaml`, `AGENTS.md`; then refresh   |
| `refresh [--upgrade]`               | bring the pack from a registry into `.pack/packs`, lock its digest; make the `id` of `state: target` |
| `values write`                      | `values.yaml` from the pack's schema, documented, the values kept                                    |
| `check [dir\|ref]`                  | author checks on a pack; with no argument, also render the release's values against it               |
| `plan`                              | the saga between the installed version and the wanted one, with drift                                |
| `apply`                             | run the saga                                                                                         |
| `delete`, `rollback`                | take the installation away; take back an unfinished saga                                             |
| `template [--key K] [-o dir]`       | the rendered config files, nothing installed                                                         |
| `graph [-f dot\|mermaid]`           | keys, dependencies, savepoints                                                                       |
| `log [--list\|--node K\|--prune N]` | the journal of a saga                                                                                |

Ctrl-C cancels the command's context: running steps are cut short and the saga is rolled back; a second signal
ends the process, and the record stays `in-progress` for `rollback` or the next `apply`.

## Layout

```
cmd/            the commands (cobra), nothing else
internal/       release, session (a command's connection to the world), workdir, oci, values, interrupt, testpacks
sdk             author side: Pack, Meta, schema generation, packschema (PackRoot vocabulary), compute (CEL)
sdk/python      the same author side in Python (packsdk); sdk/java the same in Java (io.horchestra.pack.sdk)
pkg/bundle      a pack read from disk: the virtual composition of the pack and the packs it holds
pkg/render      values → units, files, report (configure → resolve → collect → mutate → validate → convert)
pkg/plan        the plan between two versions (changes, hooks, savepoints, drift)
pkg/graph       the dependency graph; pkg/runner runs it; pkg/saga makes it transactional (journal, store, rollback)
pkg/adapter     graph nodes ↔ plan steps ↔ target steps
pkg/target      what a target must do (Resolve, Execute; optional Recorder, Observer); target/memory runs nothing;
                target/remote starts a target that is a program of its own (gRPC, proto/target.proto) and serves one
examples/       postgres, written in Go, Python and Java; the golden files all three are compared with
pkg/environment env.yaml; pkg/format names of artifacts and annotations
```

## The flow of `apply`

`session.Open` reads the environment and binds the target → `Prepare` reads the record, renders the installed
version with its recorded values and the wanted one with the release's, looks at what the target holds (drift) and
builds the plan → `Apply` makes a graph of the keys (savepoints are nodes), runs it with the saga: a failure takes
back what is not behind a savepoint, `Keep` and `no_rollback` change that; the keys the new version no longer has
are removed afterwards.

## Registries

`oci-packer` (`github.com/arenadata/oci-packer`) does all the registry work. `pack init` (and `pack refresh` after it) resolves a tag to a digest
and extracts the pack with the packs mounted in it into `.pack/packs/<digest>/<name>/`; every other command reads
that directory, so a release gives the same result whatever the tag becomes. `oci-packer get oci://host/repo:tag:path`
reads one file of a pack without extracting it.

## Tests

```
go test -race ./...
```

`internal/testpacks` are packs written the way an author writes them (`tembo`, `openbao`, which holds tembo);
`regserver` serves them from an in-process registry.
