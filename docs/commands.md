# Commands

```
pack [-C dir] [--env name] <command>
```

`-C` runs in another directory; `--env x` reads `env.x.yaml` instead of `env.yaml` (`PACK_ENV`). Registries:
Registry credentials are in the variables `release.yaml` names (`username_env`, `password_env`, see [release.md](release.md)).

| command                                     |                                                                                                                                                                                                                                                                 |
|---------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `init [oci://host/repo:tag]`                | bring the pack from a registry into `.pack/packs`, lock its digest in `release.lock.json`; with a reference, write it into `release.yaml`. With `state: target`, make the installation's `id`. `--upgrade` takes the digest the tag has now, not the locked one |
| `check [dir\|ref] [--strict]`               | the author checks on a pack. With no argument it also renders the release's values against the pack and says what is wrong with them. What a pack's `docs/` does not say is a warning; `--strict` makes it a problem                                           |
| `docs skeleton -l lang <dir> [-o file]`     | `docs/<lang>.yaml` for a generated pack: every field, group and rule, with what the schema says in the language and `""` where it says nothing ([authoring.md](authoring.md#documentation))                                                                      |
| `plan [-o file]`                            | the saga: a first installation, or the upgrade between the installed version and the wanted one, with the drift the target reports. `-o` also writes the plan as YAML                                                                                           |
| `apply`                                     | run the saga. The target says what is installed; pack drives it step by step                                                                                                                                                                                    |
| `delete [--dry-run]`                        | remove the installation: the workloads go, **the data and the credentials stay**                                                                                                                                                                                |
| `rollback [--dry-run]`                      | take an installation back: undo an unfinished saga, or run a saga back to the version the last one replaced                                                                                                                                                     |
| `template [--key K] [-o dir]`               | the config files of the release, rendered, nothing installed. `-o` writes `<key>/<path>`; `--key` limits it to a key                                                                                                                                            |
| `graph [-f dot\|mermaid]`                   | the keys, what each waits for, the savepoints                                                                                                                                                                                                                   |
| `log [run] [--list] [--node K] [--prune N]` | the journal of a saga: its stages and the logs of its keys. `--list` lists the sagas, `--node` shows one key's lines, `--prune N` keeps the N newest                                                                                                            |

**Ctrl-C** cancels the command's context: running steps are cut short and the saga is rolled back; a second signal ends
the process, and the record stays `in-progress` for `rollback` or the next `apply`.

A command that fails says why (`pack: …`) and exits with 1; no usage follows an error of its own, only a misuse of the
command line.
