# What plan and apply do

`pack plan` shows, and `pack apply` runs, the **saga** between what is installed and what the release wants. Both work
the same way up to the last step: the installed version is rendered with the values it was installed with, the wanted
one
with the release's, and the plan is the difference.

## The plan

```
openbao: install 2.6.5
  1. postgres         apply 0.2.0
  2. postgres         wait  ready (http:api/health)
     ── savepoint: postgres ──   unit 1–2
  3. openbao          apply 2.6.5
  4. openbao          wait  ready (http:api/v1/sys/health)
     ── end ──   unit 3–4
rollback policy: auto, to last savepoint; irreversible: none
```

Steps, per key: `apply` (the unit), `wait` (its readiness), `hook:<name>` (the pack's hooks of the phase), `remove`. A
key
the new version no longer has is removed after the rest. A first installation runs the `preInstall`/`postInstall` hooks;
a version change runs `preUpgrade`/`postUpgrade` or `preDowngrade`/`postDowngrade`, **from the version being installed**
(a downgrade runs the target version's hooks, which an older pack may not have), those whose `When` matches. A key
nothing changed in has no step. The order is the graph's: a held pack before the pack that holds it, dependents last.

`irreversible` names a step with no `Compensate`. `run.strict` refuses a plan that has one.

## Drift

A target that can say what it holds (`Observer`) is asked before the plan is built. A key whose image or replicas differ
from the record is shown as a change of kind `drift` and put back by `apply`; a target that cannot tell is not guessed
at.

## Savepoints and rollback

A **savepoint** is a held pack named in `PackRoot().Savepoints`. When it is done, it and what it waits for stay
installed through a later failure: a database whose schema was migrated is not taken back by taking back the
application.

On a failure the saga rolls back what is in effect and not behind a savepoint, newest first, by each step's
compensation:

| `run.`              |                                                                                          |
|---------------------|------------------------------------------------------------------------------------------|
| (default)           | roll back to the last savepoint; the record says what failed and what is still in effect |
| `no_rollback: true` | leave everything as it is                                                                |
| `keep: [key]`       | the rollback leaves these keys alone, savepoint or not                                   |

The record is written after every change. A saga that was cut short (Ctrl-C twice, a crash) stays `in-progress`:

- `pack apply` with the same pack and values **continues** it: the nodes in effect stay, the ones that were under way
  are undone and run again; with other values it refuses and says so;
- `pack rollback` takes it back whole (`Undo`), and for a first installation that leaves a record with the
  installation's id and its history, nothing installed.

`pack rollback` with nothing unfinished runs a saga **to the version the last one replaced**.

## What an apply does unasked

`run.approve` in `env.yaml` says it. `auto-safe`, the default, applies a plan without a risk and refuses one with
them; `plan` refuses every plan that does something; `auto` refuses none. The risks of a plan are in it (`pack plan`,
`--json`'s `risks`):

| risk           | what                                                                 |
|----------------|----------------------------------------------------------------------|
| `data`         | a key with data volumes is dropped                                   |
| `secret`       | a key's credentials change                                           |
| `downgrade`    | a key goes back a version                                            |
| `savepoint`    | an upgrade past a savepoint: a failure keeps what is before it       |
| `irreversible` | a step that cannot be undone (a hook without compensate)             |
| `action`       | an action the pack says is never run unasked (`pack run`, `approve`) |

A plan refused is not begun: the error is `needs_approval`, its risks the problems. The plan says its digest
(`plan sha256:…`), and `pack apply --approve <digest>` applies that very plan, and refuses one that changed since it
was shown. `rollback` and `delete` take `--approve` the same way, the digest
their `--dry-run` says, and a refusal names the command to run again (`pack rollback --to 3 --approve <digest>`).
The digest covers the release's revision (`PACK_REVISION`, else the commit the release's directory is at), which the
plan and the record of the saga say: a plan approved on another revision does not apply.
Taking an installation away is no risk of itself (the data and the credentials stay), and neither is undoing an
unfinished saga — recovery is not held back —, so `delete` and that `rollback` ask only under `plan`.

`pack run` is a plan of one step, the action's hook with its command filled in, by its digest (`--dry-run` says it).
An action that only reads (no `mutating`) is asked for by no policy; a `mutating` one only under `plan`; one the pack
marks `approve` under `auto-safe` too.

## Revisions

Every saga that ends is a **revision** of the installation, in the record's `history`: its number, when, the pack (by
digest, a directory by its path), the version, the values (references to credentials, never credentials) and how it
ended — `installed`, `undone` (taken back by `rollback`, with why it failed; a saga `pack delete` takes away unfinished
is `undone` too), or `removed` (`pack delete`). The record
keeps as many as `run.journal_keep` keeps sagas of the journal (20 by default); a removal keeps them too.

`pack history` lists them, the one installed now marked `*` (`--json`: `"current": true`) — the newest that installed
what is installed. `pack rollback --to <n>` goes back to a revision that was
installed: a saga from what is installed (or from nothing, after a delete) to its pack and its values, planned and run
as an apply is — the hooks of the version transition run, a savepoint limits what a failure takes back — and itself a
revision of its own. A revision of a directory pack is the directory as it is now.

A key the new version dropped is removed *before* the record says the new version is installed, so a failure there
leaves
the saga in progress and the next `apply` finishes it; nothing is lost between.

A second saga on the installation is refused while the first runs (a lock file under `.pack/`, with the owner's pid; a
crash leaves it, and the error says which file to remove). With `state: db` the lock is the database's: an advisory
lock of PostgreSQL's, a lease of `pack_locks` on SQLite — a pack on another checkout, another machine, waits on it
the same.

## The journal

Every saga is journaled under `.pack/` — with `state: db`, in the `pack_journal` table of the state database —: its
stages and the output of each key's steps. `pack log --list` lists the sagas,
`pack log [run]` shows one, `--node K` only the lines of a key. After each apply the journal keeps `run.journal_keep`
newest sagas (default 20; below 0 keeps all); `pack log --prune N` does it by hand.
