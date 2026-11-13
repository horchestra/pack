# A release

A command runs in a **release**: the directory it is started in (`-C dir` names another), the way terraform runs in a
root module. What a pack is installed from, with which values, into which environment is read from that directory, so
the same bare command gives the same result on a console and in CI. **The command line adds nothing to a release.**

| file                                      |                                                                                                                                                                                                        |
|-------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `release.yaml`                            | `pack:` a directory or `oci://host/repo:tag`; `values:` more files or directories; `state: file` (default), `target`, `db` or `observe`; `state_dsn_env:` the variable with the state DSN (`PACK_STATE_DSN`); `id:` the installation's UUID (`state: target`, `db` and `observe`; `pack refresh` writes it) |
| `values.yaml`, `*.values.yaml`, `values/` | what the operator wants, in the pack's own words, see [values.md](values.md)                                                                                                                           |
| `env.yaml`                                | the environment; `--env x` (or `PACK_ENV`) reads `env.x.yaml` instead                                                                                                                                  |
| `release.lock.json`                       | the digest a registry tag meant when `pack init` or `pack refresh` ran; commit it                                                                                                                      |
| `.pack/`                                  | the state (`state.json`), the journal, the packs brought from a registry (`packs/<digest>/`); not for version control                                                                                  |

```yaml
# release.yaml
pack: oci://registry.example/packs/openbao:2.6.5
registry:
  plain_http: false                  # how the pack is read from a registry; a target's source says it for itself
  username_env: REGISTRY_USER        # the variables that hold the credentials, never the credentials
  password_env: REGISTRY_TOKEN
```

A registry that names no variables is read anonymously: credentials reach only the registry they are written for. A
variable that is named and empty is an error when that registry is read.

## env.yaml

Where a pack is installed and how it runs, YAML or JSON. Nested keys flatten with `_` (`vault: {addr: …}` is
`vault_addr`); everything that is not pack's own is the **target's** setting.

```yaml
target: k8s                          # the system a pack is installed on
namespace: data                      # and anything else the target wants
lang: ru                             # the language of the comments pack writes (values.yaml); $LANG when unset
runtime: # where the target's program runs: native (default), docker or k8s — its image in a container or a Pod, see targets.md
  type: docker
discover: # what pack discover asks the targets of; no hosts — every host
  hosts:
    - db1
    - db2
run: # how a saga runs
  jobs: 4                            # keys at once; 0 — as many as CPUs, 1 — one by one
  strict: true                       # refuse a plan with an irreversible step
  no_rollback: false                 # leave what is in effect as it is on a failure
  keep: [ postgres ]                   # keys a rollback after a failure leaves as they are
  journal_keep: 20                   # sagas the journal keeps; below 0 — all (default 20)
  approve: auto-safe                 # what apply does unasked: auto-safe (default) | plan | auto, see saga.md
history_count: 3                     # snapshots of a managed file a target keeps (<path>.bak.N); 0 — none, and a
                                     # failed saga stops where it is (nothing to restore a file from)
secrets: # where the passwords pack creates come from
  provider: generate                 # generate | env | file:<dir>
  provision: [ /postgres/pg_password ]
store: # where secrets are kept outside of any one target
  provider: openbao                  # openbao | vault | file
  addr: https://bao.example:8200
  mount: secret
  prefix: pack/prod
  cacert: /etc/pack/ca.crt
  # provider file keeps them as plaintext files instead: path: <dir> — a secret <dir>/<name>.json, the
  # directory 0700, the files 0600; for a machine of one's own, not for production (pack says so at every start)
vault: # a Vault KV read over the file, the last path winning
  addr: https://vault.example:8200   # VAULT_ADDR when the file has none
  paths: [ secret/data/pack/prod ]     # needs VAULT_TOKEN; the token is never in the file
targets: # more targets, by the name a key's `target` gives (no underscore in a name)
  edge: { target: host, network: edge }
keys: # how the environment runs each key of the pack
  postgres:
    replicas: 3
    resources: { requests: { cpu: "1", memory: 2Gi }, limits: { memory: 2Gi } }
    env: [ { name: TZ, value: UTC } ]
    labels: { tier: data }
    target: edge
    reach: { instances: [ db1.example, db2.example ], ports: { pg: 5432 } }
```

- `target:` names a target; the core knows none by name. `memory` is in pack (it keeps what it is told in the
  process: what the tests and `plan`/`template` use; `observe: "true"` makes it report what it holds, so drift can be
  seen; `fail_at: <key>/<kind>` fails a step, `hold_at: <key>/<kind>` keeps one running until it is cut short — stands
  of a failure and of an interrupt). `host` and `k8s` are programs of their own that `pack refresh` (and
  `pack init`) downloads and
  pack starts,
  `pack-target-<name>`, see [targets.md](targets.md); each one's settings and the entries of `keys:` it reads are in its
  README.
- The default target is the one the top-level keys describe; each entry of `targets` is a target of its own, none
  inherited from the default one. `target:` may name an entry instead (`target: main`,
  `targets: {main: {target: host}}`):
  that entry is the default target, and its settings are written there, not at the top.
- `keys` is where placement lives: **values configure the application, the environment runs it.** `replicas` is how many
  instances run; `resources` are cgroup requests and limits (a pack's `Minimum` is their lower bound); `env` is added to
  the component's own; the rest of an entry is whatever the key's target has words for.
- From Vault (and from the overrides) `keys` comes as one field `keys` holding the document as text; it is merged over
  the file's entry by entry, field by field.
- The store's token comes from `PACK_STORE_TOKEN`, else `VAULT_TOKEN`.

## The state

`state: file` keeps the record of the installation in `.pack/state.json` (mode 0600; references to credentials, never
the
credentials). `state: target` has the target keep it, so a CI job with no disk finds the installation where it is;
without a file for it, the installation's `id` is written to `release.yaml` by `pack refresh`.

`state: db` keeps the record, the sagas' journals and the targets' facts in a database — PostgreSQL or SQLite —
whose DSN is read from the environment variable `state_dsn_env` names (`PACK_STATE_DSN` by default):
`postgres://…` or `sqlite://<path>`. pack owns the schema and applies the missing migrations at the first
connection. With the state in a database pack is stateless: a second process, on another checkout of the same
release, sees what the first installed and waits on its lock. `pack state migrate [--to db|file]` moves the record
and the journal between the file and the database, the history whole.

`state: observe` keeps no record at all — the self-deploy of an application on its own host, where everything the
install created is on that host and a written copy of it only drifts away from the truth. The plan is derived from
what the target says it holds (a target that cannot tell — no `inspect` capability — is refused): a key already
there exactly as rendered is no change and runs nothing, one that differs is applied with its notify hooks,
one that is absent is installed with its hooks. An embedded apply can explicitly force selected keys: then even a
matching key is applied with its notify hooks, for an operator's explicit redeploy or reconfiguration request. A saga cut short is replanned from the same observation. The
installation's `id` is the release's (`pack refresh` writes it), and `history_count` (env.yaml) is the only memory
a rollback has: a managed file's snapshots `<path>.bak.1…N`, rotated before an overwrite that changes it; with
`history_count: 0` a failed saga stops where it is and compensates nothing. `pack delete` renders what the release
wants and takes that away; `--destroy` takes the OS packages too.

The packs brought from a registry and the targets' programs are the cache's, not the release's: `PACK_CACHE`
(`~/.cache/pack` by default), shared by every release on the machine.

The record names the installation (a UUID that outlives its removal: what stays after `delete` is found again by the
next
install), the pack, the version and the values installed, the version the last saga replaced (what `rollback` goes
back to), and, while a saga runs, how far it got.
