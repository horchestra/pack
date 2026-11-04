# Commands

```
pack [-C dir] [--env name] <command>
```

`-C` runs in another directory; `--env x` reads `env.x.yaml` instead of `env.yaml` (`PACK_ENV`). Registries:
Registry credentials are in the variables `release.yaml` names (`username_env`, `password_env`,
see [release.md](release.md)).

| command                                                                                                                                                                                                                                              |                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `init <oci://…\|dir> [dir]`                                                                                                                                                                                                                          | make a release of the pack in the directory: `release.yaml`, `env.yaml` (`--env x`: `env.x.yaml`) with the target (`--target`, asked in a terminal; `--target-source` its program, `--target-image` its image, `--runtime docker` to run it from the image), a documented `values.yaml` left to the defaults, `AGENTS.md` (how an agent works with the release); then what `refresh` does. Files that are there stay without `--force`                                                                                                       |
| `refresh [--upgrade]`                                                                                                                                                                                                                                | bring the pack from a registry into `.pack/packs`, lock its digest in `release.lock.json`, download the targets' programs, lock the digest of the image of a target that runs from one (`runtime: docker`); with `state: target`, make the installation's `id`. Safe to run again; `--upgrade` takes what the tags mean now, not the locked digests                                                                                                                                                                                          |
| `check [dir\|ref] [--strict]`                                                                                                                                                                                                                        | the author checks on a pack. With no argument it also renders the release's values against the pack and says what is wrong with them. What a pack's `docs/` does not say is a warning; `--strict` makes it a problem                                                                                                                                                                                                                                                                                                                         |
| `values write [--rewrite] [--check] [--lang L]`                                                                                                                                                                                                      | the release's `values.yaml` from the pack's schema: every setting documented, the release's values kept, the rest commented lines to uncomment ([values.md](values.md#valuesyaml-written-by-pack)). A comment of the operator's stops it; `--rewrite` drops it                                                                                                                                                                                                                                                                               |
| `fill [--accept-suggested] [--rewrite]`                                                                                                                                                                                                              | what the values do not give and should: the settings required, the ones the pack suggests a value for (from the facts, `discover`) — asked one by one in a terminal, listed with `--json` (pointer, reason, schema, answers offered); `--accept-suggested` takes the suggestions                                                                                                                                                                                                                                                             |
| `answer <pointer> <value>` \| `--answers f` \| `--patch f`                                                                                                                                                                                           | a value for a setting into `values.yaml`, read as the field's type (a credential: `name/key`); `--patch` a JSON Patch; `--source operator\|llm` says who answered. A value the schema or a rule refuses is not written; `--rewrite` writes over comments of the operator's own                                                                                                                                                                                                                                                               |
| `values explain <pointer>`                                                                                                                                                                                                                           | a setting: its value and where it came from, its default, formula and the answers offered, its documentation                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `docs skeleton -l lang <dir> [-o file]`                                                                                                                                                                                                              | `docs/<lang>.yaml` for a generated pack: every field, group and rule, with what the schema says in the language and `""` where it says nothing ([authoring.md](authoring.md#documentation))                                                                                                                                                                                                                                                                                                                                                  |
| `plan [-o file]`                                                                                                                                                                                                                                     | the saga: a first installation, or the upgrade between the installed version and the wanted one, with the drift the target reports. `-o` also writes the plan as YAML                                                                                                                                                                                                                                                                                                                                                                        |
| `apply [--approve D]`                                                                                                                                                                                                                                | run the saga. The target says what is installed; pack drives it step by step. A plan `run.approve` does not let it apply unasked (risks under `auto-safe`, any under `plan`) needs `--approve` with its digest ([saga.md](saga.md#what-an-apply-does-unasked))                                                                                                                                                                                                                                                                               |
| `delete [--dry-run] [--approve D]`                                                                                                                                                                                                                   | remove the installation: the workloads go, **the data and the credentials stay**                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `rollback [--dry-run] [--to N] [--approve D]`                                                                                                                                                                                                        | take an installation back: undo an unfinished saga, or run a saga back to the version the last one replaced; `--to N` to revision N of `history`, its pack and its values ([saga.md](saga.md#revisions))                                                                                                                                                                                                                                                                                                                                     |
| `history [--json]`                                                                                                                                                                                                                                   | the revisions of the installation — every saga that ended, and the removals — the one installed now marked `*`                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `template [--key K] [-o dir]`                                                                                                                                                                                                                        | the config files of the release, rendered, nothing installed. `-o` writes `<key>/<path>`; `--key` limits it to a key                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `graph [-f dot\|mermaid]`                                                                                                                                                                                                                            | the keys, what each waits for, the savepoints                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `discover [--save-fixture f]`                                                                                                                                                                                                                        | ask the targets the facts of where they install — hosts, OS, memory, disks, ports, a cluster's storage classes — read only, into `.pack/facts/<target>.json` (`discover.hosts` in `env.yaml`: which hosts); formulas read them as `facts`, `next` says how old they are                                                                                                                                                                                                                                                                      |
| `next`                                                                                                                                                                                                                                               | where the release is — `new`, `unrefreshed`, `unfilled`, `invalid`, `failed`, `planned`, `applied` — what stops it, and the commands to run next, worked out from the files, the lock, the record and a plan                                                                                                                                                                                                                                                                                                                                 |
| `diagnose [--apply rN]`                                                                                                                                                                                                                              | what failed and why: the key and its error, the lines that show it (the journal's, the tail of its logs), what kind of failure (the pack's own diagnoses first, then `port_conflict`, `image_pull`, `oom`, `disk`, `permission`, `timeout`; `unknown` — a person should look, as for a case with no way out), the ways out; `--apply` takes one (a value: as `answer` writes it; a command: said, not run)                                                                                                                                   |
| `test <pack> [scenario…] [--update]` \| `test --release <dir> [scenario…]`                                                                                                                                                                           | the pack's scenarios (`tests/*.yaml`): facts, values, an environment, a version installed first, a failure injected — and what they give: suggestions, problems, files, the plan, how the apply ends, the diagnosis; on the memory target, nothing installed; `--release` on a release's target, for real, taken away after ([authoring.md](authoring.md#testing))                                                                                                                                                                           |
| `author check [src] [--lang L]` · `author cel <expr> --pack D [--key K] [--values f] [--facts f] [--env f]` · `author ref <topic>` · `author example <kind>` · `author docs [src] -l L` · `author scaffold --spec f \| --from D [--lang L] [-o dir]` | the author's tools, for a person or a model writing a pack: generate from the source with its SDK, `check`, the docs checks and the scenarios of `tests/` in one go — each problem with the field's pointer and the source's file:line; an expression worked out as a formula of the key would; the part of the guide a topic is; an example's source; the `docs/<lang>.yaml` to fill; a source in a language from a short spec (`--schema` its JSON Schema) or from a generated pack ([authoring.md](authoring.md#scaffolding-from-a-spec)) |
| `mcp`                                                                                                                                                                                                                                                | an MCP server on stdio for a model that runs the release (a local one — Ollama, llama.cpp — as much as any): `next`, `questions`, `answer`, `check`, `plan`, `apply`, `diagnose`, `rollback`, `status`, `run` — and the author's, `author_check`, `author_cel`, `author_ref`, `author_example`, `author_docs` — each `pack <command> --json` in the release, answered with its document (`{ok, result, log}`; `log` the last lines said, on a failure). No tool approves (`--approve` is a person's) or takes a secret's value               |
| `status`                                                                                                                                                                                                                                             | how the installation is now, asking the targets: the version installed, the drift from the record, the health of each key's instances (`health` capability); `healthy` all of it in order — `next` says `failed` when it is not                                                                                                                                                                                                                                                                                                              |
| `run [action] [--key K] [--param k=v]… [--dry-run] [--approve D]`                                                                                                                                                                                    | an action of the pack (`PackActions`: backup, restore …) on a key of what is installed, run in the key's first instance as it runs, into the journal; without one, the list with their parameters. A `mutating` action is a plan to `run.approve`, one marked `approve` a risk ([saga.md](saga.md#what-an-apply-does-unasked))                                                                                                                                                                                                               |
| `log [run] [--list] [--node K] [--prune N]`                                                                                                                                                                                                          | the journal of a saga: its stages and the logs of its keys. `--list` lists the sagas, `--node` shows one key's lines, `--prune N` keeps the N newest                                                                                                                                                                                                                                                                                                                                                                                         |
| `version`                                                                                                                                                                                                                                            | the version of the binary; `--json`: `{"version": …, "formats": {"json": 1, "stream": 1}}` — the versions of the documents pack writes, for a program that reads them to check against                                                                                                                                                                                                                                                                                                                                                       |

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
    "problems": [
      {
        "kind": "required",
        "where": "/openbao/api_addr",
        "message": "missing property 'api_addr'"
      }
    ]
  },
  "next": [
    "edit values.yaml",
    "pack check"
  ]
}
```

and exits with 1 (in Go, `pkg/api`: `Failure`, and `Event` of `--stream` below). The result documents are Go types of
`pkg/api` too — `InstalledResult`, `RemovedResult`, `UndoneResult`, `RanResult`, `ActionsResult`, `Status`,
`Revision`, `LogResult`, `TemplateResult`, `GraphResult`, `CheckResult`, `DiscoverResult`, `Explanation`, `NextState`,
`VersionResult` — what a program reads a command's document into. The codes are a closed list:

| code                | what                                                   | next                             |
|---------------------|--------------------------------------------------------|----------------------------------|
| `no_release`        | no `release.yaml`, or it names no pack                 | `pack init <pack> .`             |
| `not_refreshed`     | the pack or a target is not brought into `.pack`       | `pack refresh`                   |
| `values_missing`    | settings the schema requires are not given             | edit `values.yaml`, `pack check` |
| `values_invalid`    | the schema, a rule of the pack or the resources refuse | edit `values.yaml`, `pack check` |
| `check_failed`      | `pack check` found problems                            | `pack check`                     |
| `saga_failed`       | a saga failed; the record says what is in effect       | `pack log`, `apply`, `rollback`  |
| `saga_unfinished`   | a saga did not finish                                  | `pack log`, `apply`, `rollback`  |
| `nothing_installed` | nothing to remove or roll back                         | `pack plan`, `pack apply`        |
| `foreign_comments`  | `values.yaml` has comments pack did not write          | `pack values write --rewrite`    |
| `target`, `error`   | the target refused; anything else                      |                                  |

A problem is `{kind, where, rule?, message}`: `kind` is `required`, `schema`, `rule` or `environment` for the values
(`where` the operator's JSON Pointer, or the environment's setting), `check` or `warning` for what `pack check` found
— a failing check's problems are in its error document, the one document it writes. `target` is what the target
itself said: it refused, or did not start or answer.

## --stream

`--stream` (of `apply`, `delete`, `rollback`, `plan` and `log`) makes stdout NDJSON — one event a line, as the command
does what it says — and nothing else goes there: the text a person reads goes to stderr. A UI's backend runs pack and
forwards each line as it comes (an SSE `data:`, a WebSocket frame) without reading text; it cancels with SIGINT, as
Ctrl-C does — the saga cuts the running steps short and rolls back, and says so in events. Not with `--json`: the
last event is the result `--json` writes.

```json
{"seq":5,"time":"2026-11-04T10:00:01Z","type":"node","key":"openbao","status":"running","saga":"20261104T100000.512Z"}
{"seq":6,"time":"2026-11-04T10:00:01Z","type":"log","key":"openbao","step":"apply","message":"container openbao started","saga":"20261104T100000.512Z"}
```

| field              | what                                                                                                    |
|--------------------|---------------------------------------------------------------------------------------------------------|
| `seq`              | 1, 2, … in the order written: an SSE id, what `Last-Event-ID` resumes from                              |
| `time`             | when it happened (UTC)                                                                                  |
| `type`             | `plan`, `node`, `log`, `saga`, `result`                                                                 |
| `key`, `step`      | the key an event is of, and the step of it a line is of (`apply`, `wait`, `hook migrate`, `undo apply`) |
| `status`           | of a `node`, a `saga`, a `result`, below                                                                |
| `message`, `error` | what was said, what failed                                                                              |
| `saga`             | the saga's id: `pack log <it>`                                                                          |
| `plan`             | of a `plan` event: the plan, as `plan --json` writes it                                                 |
| `result`           | of an `ok` result: the command's document, as `--json` writes it                                        |
| `code`, `next`     | of a `failed` result: the error's code and what to run next, as `--json` says them                      |

| type     | status                                                                                                                         |
|----------|--------------------------------------------------------------------------------------------------------------------------------|
| `plan`   | — the plan the command runs, before anything is done (`plan`, `apply`; the removal of `delete`, the taking back of `rollback`) |
| `node`   | `running`, `done`, `failed`; `compensating` → `rolled-back` (a rollback, an undo); `removing` → `removed` (`delete`)           |
| `log`    | — a line of a key's step (what the target said) or of the saga                                                                 |
| `saga`   | `started`, `failed` (a key failed: the rollback follows), `ended` (or `failed` if recording the end failed), `interrupted`     |
| `result` | `ok` or `failed` — always the last event; the exit code is the command's                                                       |

A reader that goes away stops the events, not the command: a saga goes on to its end or its rollback, the text on
stderr says so (pack ignores SIGPIPE while it streams). A removal's events are of its keys alone — no savepoint is
taken away. `pack test` has no `--stream`: its scenarios run on a stand of their own, its result is one document.

A saga's events are the entries of its journal: `pack log --stream [run]` gives a saga that is over in the same
events, `result` last — what a UI shows again when it lost the stream. pack writes no value of a setting into an
event (a plan has none); a `log` line is what a target said, the target's to keep clean of secrets.

## The log

What a command says to a person — its text, `--json`, `--stream` — is not the log. The log is what is under it, for
whoever looks into what pack does. It goes to stderr, at a level: `--log-level` (or `PACK_LOG_LEVEL`) is `trace`,
`debug`, `info`, `warn` (the default: what goes wrong, nothing of what goes right), `error` or `off`. `--log-format`
(or `PACK_LOG_FORMAT`) is `console`, a line a person reads, or `json`, an object a line.

| level   | what pack logs                                                                                                                                                                                                                                                                              |
|---------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `warn`  | a call to a target that failed, a step that failed, a line a target wrote that is not of its log                                                                                                                                                                                            |
| `debug` | the command and how long it took; the release and the environment; each target started, its calls (method, time, code) and steps; the registry's resolves and pulls; the secret store's reads and writes (the path and the number of fields); the saga's entries; the tools `pack mcp` runs |
| `trace` | what a step says as the target says it; each request to the secret store, a target's download, the cluster (method, URL without its query, status, time); the size of each call to a target; each CEL formula and rule evaluated (the expression, not its value)                            |

The targets log at the level pack has: pack gives it to each one it starts (`PACK_LOG_LEVEL`, `PACK_LOG_FORMAT=json`
in its environment — a process's, a container's, a Pod's), reads what the target writes and logs each line again
under its own log, `target=<name>`, at the level the line says (a line that is not JSON, a panic, at `warn`). A
target in a Pod has its log followed by pack. `pack mcp` runs its commands at its own level, logs their log again,
and gives a model what they said, with what of their log is `warn` or above.

No secret is logged: no token, no value of a secret or of a setting, no payload of a call, no header of a request.
