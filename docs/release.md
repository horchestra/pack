# A release

A command runs in a **release**: the directory it is started in (`-C dir` names another), the way terraform runs in a
root module. What a pack is installed from, with which values, into which environment is read from that directory, so
the same bare command gives the same result on a console and in CI. **The command line adds nothing to a release.**

| file                                      |                                                                                                                                                                                                     |
|-------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `release.yaml`                            | `pack:` a directory or `oci://host/repo:tag`; `values:` more files or directories; `state: file` (default) or `target`; `id:` the installation's UUID (`state: target` only; `pack init` writes it) |
| `values.yaml`, `*.values.yaml`, `values/` | what the operator wants, in the pack's own words, see [values.md](values.md)                                                                                                                        |
| `env.yaml`                                | the environment; `--env x` (or `PACK_ENV`) reads `env.x.yaml` instead                                                                                                                               |
| `release.lock.json`                       | the digest a registry tag meant when `pack init` ran; commit it                                                                                                                                     |
| `.pack/`                                  | the state (`state.json`), the journal, the packs brought from a registry (`packs/<digest>/`); not for version control                                                                               |

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
run: # how a saga runs
  jobs: 4                            # keys at once; 0 — as many as CPUs, 1 — one by one
  strict: true                       # refuse a plan with an irreversible step
  no_rollback: false                 # leave what is in effect as it is on a failure
  keep: [ postgres ]                   # keys a rollback after a failure leaves as they are
  journal_keep: 20                   # sagas the journal keeps; below 0 — all (default 20)
secrets: # where the passwords pack creates come from
  provider: generate                 # generate | env | file:<dir>
  provision: [ /postgres/pg_password ]
store: # where secrets are kept outside of any one target
  provider: openbao
  addr: https://bao.example:8200
  mount: secret
  prefix: pack/prod
  cacert: /etc/pack/ca.crt
vault: # a Vault KV read over the file, the last path winning
  addr: https://vault.example:8200   # VAULT_ADDR when the file has none
  paths: [ secret/data/pack/prod ]     # needs VAULT_TOKEN; the token is never in the file
targets: # more targets, by the name a key's `target` gives (no underscore in a name)
  edge: { target: docker, network: edge }
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
  seen). `docker`, `ansible` and `k8s` are programs of their own that `pack init` downloads and pack starts,
  `pack-target-<name>`, see [targets.md](targets.md); each one's settings and the entries of `keys:` it reads are in its README.
- The default target is the one the top-level keys describe; each entry of `targets` is a target of its own, none
  inherited from the default one. `target:` may name an entry instead (`target: main`, `targets: {main: {target: docker}}`):
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
without a file for it, the installation's `id` is written to `release.yaml` by `pack init`.

The record names the installation (a UUID that outlives its removal: what stays after `delete` is found again by the
next
install), the pack, the version and the values installed, the version the last saga replaced (what `rollback` goes
back to), and, while a saga runs, how far it got.
