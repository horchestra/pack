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
crash leaves it, and the error says which file to remove).

## The journal

Every saga is journaled under `.pack/`: its stages and the output of each key's steps. `pack log --list` lists the
sagas,
`pack log [run]` shows one, `--node K` only the lines of a key. After each apply the journal keeps `run.journal_keep`
newest sagas (default 20; below 0 keeps all); `pack log --prune N` does it by hand.
