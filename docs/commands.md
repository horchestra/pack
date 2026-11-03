# Commands

```
pack [-C dir] [--env name] <command>
```

`-C` runs in another directory; `--env x` reads `env.x.yaml` instead of `env.yaml` (`PACK_ENV`). Registries:
Registry credentials are in the variables `release.yaml` names (`username_env`, `password_env`, see [release.md](release.md)).

| command                                     |                                                                                                                                                                                                                                                                 |
|---------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `init <oci://…\|dir> [dir]`                  | make a release of the pack in the directory: `release.yaml`, `env.yaml` (`--env x`: `env.x.yaml`) with the target (`--target`, asked in a terminal; `--target-source` its program, `--target-image` its image, `--runtime docker` to run it from the image), a documented `values.yaml` left to the defaults, `AGENTS.md` (how an agent works with the release); then what `refresh` does. Files that are there stay without `--force` |
| `refresh [--upgrade]`                       | bring the pack from a registry into `.pack/packs`, lock its digest in `release.lock.json`, download the targets' programs, lock the digest of the image of a target that runs from one (`runtime: docker`); with `state: target`, make the installation's `id`. Safe to run again; `--upgrade` takes what the tags mean now, not the locked digests |
| `check [dir\|ref] [--strict]`               | the author checks on a pack. With no argument it also renders the release's values against the pack and says what is wrong with them. What a pack's `docs/` does not say is a warning; `--strict` makes it a problem                                           |
| `values write [--rewrite] [--check] [--lang L]` | the release's `values.yaml` from the pack's schema: every setting documented, the release's values kept, the rest commented lines to uncomment ([values.md](values.md#valuesyaml-written-by-pack)). A comment of the operator's stops it; `--rewrite` drops it                       |
| `fill [--accept-suggested] [--rewrite]`     | what the values do not give and should: the settings required, the ones the pack suggests a value for (from the facts, `discover`) — asked one by one in a terminal, listed with `--json` (pointer, reason, schema, answers offered); `--accept-suggested` takes the suggestions |
| `answer <pointer> <value>` \| `--answers f` \| `--patch f` | a value for a setting into `values.yaml`, read as the field's type (a credential: `name/key`); `--patch` a JSON Patch; `--source operator\|llm` says who answered. A value the schema or a rule refuses is not written; `--rewrite` writes over comments of the operator's own |
| `values explain <pointer>`                  | a setting: its value and where it came from, its default, formula and the answers offered, its documentation                                                                                                                                                     |
| `docs skeleton -l lang <dir> [-o file]`     | `docs/<lang>.yaml` for a generated pack: every field, group and rule, with what the schema says in the language and `""` where it says nothing ([authoring.md](authoring.md#documentation))                                                                      |
| `plan [-o file]`                            | the saga: a first installation, or the upgrade between the installed version and the wanted one, with the drift the target reports. `-o` also writes the plan as YAML                                                                                           |
| `apply`                                     | run the saga. The target says what is installed; pack drives it step by step                                                                                                                                                                                    |
| `delete [--dry-run]`                        | remove the installation: the workloads go, **the data and the credentials stay**                                                                                                                                                                                |
| `rollback [--dry-run] [--to N]`             | take an installation back: undo an unfinished saga, or run a saga back to the version the last one replaced; `--to N` to revision N of `history`, its pack and its values ([saga.md](saga.md#revisions))                                                          |
| `history [--json]`                          | the revisions of the installation — every saga that ended, and the removals — the one installed now marked `*`                                                                                                                                                  |
| `template [--key K] [-o dir]`               | the config files of the release, rendered, nothing installed. `-o` writes `<key>/<path>`; `--key` limits it to a key                                                                                                                                            |
| `graph [-f dot\|mermaid]`                   | the keys, what each waits for, the savepoints                                                                                                                                                                                                                   |
| `discover [--save-fixture f]`               | ask the targets the facts of where they install — hosts, OS, memory, disks, ports, a cluster's storage classes — read only, into `.pack/facts/<target>.json` (`discover.hosts` in `env.yaml`: which hosts); formulas read them as `facts`, `next` says how old they are |
| `next`                                      | where the release is — `new`, `unrefreshed`, `unfilled`, `invalid`, `failed`, `planned`, `applied` — what stops it, and the commands to run next, worked out from the files, the lock, the record and a plan                                                     |
| `diagnose [--apply rN]`                     | what failed and why: the key and its error, the lines that show it (the journal's, the tail of its logs), what kind of failure (the pack's own diagnoses first, then `port_conflict`, `image_pull`, `oom`, `disk`, `permission`, `timeout`; `unknown` — a person should look, as for a case with no way out), the ways out; `--apply` takes one (a value: as `answer` writes it; a command: said, not run) |
| `log [run] [--list] [--node K] [--prune N]` | the journal of a saga: its stages and the logs of its keys. `--list` lists the sagas, `--node` shows one key's lines, `--prune N` keeps the N newest                                                                                                            |

**Ctrl-C** cancels the command's context: running steps are cut short and the saga is rolled back; a second signal ends
the process, and the record stays `in-progress` for `rollback` or the next `apply`.

A command that fails says why (`pack: …`) and exits with 1; no usage follows an error of its own, only a misuse of the
command line.

## --json

`--json` makes a command's result one JSON document on stdout — the plan of `plan`, what `apply` installed, the problems
of `check`, the files of `template`, the nodes of `graph`, the entries of `log` (the last 1000, `truncated` beyond) —
and nothing else goes there: what is said along the way, the saga's events with it, goes to stderr. A command that
fails writes

```json
{
  "error": {
    "code": "values_missing",
    "message": "values do not validate: …",
    "problems": [{"kind": "required", "where": "/openbao/api_addr", "message": "missing property 'api_addr'"}]
  },
  "next": ["edit values.yaml", "pack check"]
}
```

and exits with 1. The codes are a closed list:

| code                | what                                                      | next                                |
|---------------------|-----------------------------------------------------------|-------------------------------------|
| `no_release`        | no `release.yaml`, or it names no pack                    | `pack init <pack> .`                |
| `not_refreshed`     | the pack or a target is not brought into `.pack`          | `pack refresh`                      |
| `values_missing`    | settings the schema requires are not given                | edit `values.yaml`, `pack check`    |
| `values_invalid`    | the schema, a rule of the pack or the resources refuse    | edit `values.yaml`, `pack check`    |
| `check_failed`      | `pack check` found problems                               | `pack check`                        |
| `saga_failed`       | a saga failed; the record says what is in effect          | `pack log`, `apply`, `rollback`     |
| `saga_unfinished`   | a saga did not finish                                     | `pack log`, `apply`, `rollback`     |
| `nothing_installed` | nothing to remove or roll back                            | `pack plan`, `pack apply`           |
| `foreign_comments`  | `values.yaml` has comments pack did not write             | `pack values write --rewrite`       |
| `target`, `error`   | the target refused; anything else                         |                                     |

A problem is `{kind, where, rule?, message}`: `kind` is `required`, `schema`, `rule` or `environment` for the values
(`where` the operator's JSON Pointer, or the environment's setting), `check` or `warning` for what `pack check` found
— a failing check's problems are in its error document, the one document it writes. `target` is what the target
itself said: it refused, or did not start or answer.
