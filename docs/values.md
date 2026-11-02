# Values and the effective document

## What the operator writes

One tree, shaped by the pack's schema, in the pack's own words (the application's keys, not the platform's):

```yaml
openbao:
  api_addr: https://bao:8200
  ui: true
postgres: # a held pack; named even as `{}` it is selected, and its defaults and formulas stay
  id: n1
```

The tree may be one file (`values.yaml`), several (`*.values.yaml`) or a directory (`values/`), plus the files
`release.yaml` names under `values:`. They are merged deeply; **values two files give differently are refused**,
whatever the order of the names — nothing is overridden silently. The command line adds nothing.

A credential is only ever a reference, `{secretRef: {name, key}}`; a plain string in a `Secret` leaf is refused.
Replicas, resources and placement are not values: they are the environment's, see [release.md](release.md#envyaml).

## How the effective document is built

1. **defaults** — the schema's `default`s: what `SetDefaults` left non-zero;
2. **formulas** — `PackCompute`, for leaves the operator did not write;
3. **values** — the operator's tree, always on top.

Objects merge deeply, **arrays are replaced whole**, `null` is a value, not a deletion. The schema validates the
**effective** document: a required leaf closed by a default or a formula passes. `PackRules` are checked on the values
as the operator wrote them (`preMutate`, the default) or on the effective document (`postMutate`).

A formula runs after every value it reads is settled; two formulas that read each other are refused as a cycle. A result
of `null` leaves the leaf as it was.

## The CEL environment

| variable                                                                                 |                                                                                                                                      |
|------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------|
| `values`                                                                                 | the whole document of the pack                                                                                                       |
| `limits`                                                                                 | what the target says of the key's limits: `memory` in bytes, `cpu` in millicores; absent where there is none                         |
| `me`                                                                                     | the key itself: `service`, `instances`, `replicas`, `ports`, and `reach` where its target says how it is reached from another target |
| `pack.installation`, `pack.component`, `pack.namespace`, `pack.service`, `pack.replicas` | where and as what the key is installed                                                                                               |
| `pack.values.<key>`                                                                      | any key's own values, read without binding a role                                                                                    |
| `pack.keys.<key>`                                                                        | any key as `me` describes it: `service`, `instances`, `replicas`, `ports`                                                            |

`instances` are names, never addresses: stable under any network mode and known before anything is scheduled. A key on
another target is described by where it is *reached from outside* (`reach`), which its own target says.

`pack.values.<key>` reads the values of the key as the operator and its formulas settled them; a formula that reads it
waits for the formulas that do not. **A password leaf is not there**, not even as its `secretRef`: a derived value is
stored, and a credential must not be one. Guard a path a key may not have with `has(pack.values.postgres)`.

Two functions beyond CEL's own: `quantity("1Gi")` (bytes or millicores as an integer) and `indexMap(list, from)`
(a list into a map by position). Nothing in the environment reads the clock, the network or the disk.

## Credentials the tool creates

A password leaf the operator did not fill can be made for them. `env.yaml` names which leaves
(`secrets.provision: [/postgres/pg_password]`) and where the value comes from (`secrets.provider`):

| provider     |                                                                   |
|--------------|-------------------------------------------------------------------|
| `generate`   | a random value per leaf                                           |
| `env`        | `$PACK_SECRET_<POINTER>`, e.g. `PACK_SECRET_POSTGRES_PG_PASSWORD` |
| `file:<dir>` | `<dir>/<pointer with dots>`                                       |

Only the listed leaves are read; a pointer must be a password leaf the operator did not already fill. The reference is
written into the values the installation records, so the next run finds the same secret, and the target creates the
secret. The record keeps references, never values.

With a **secret store** (`store:` in `env.yaml`, provider `openbao`) the value lives there under the installation's
id, and a target that can have the store deliver it need not be given it. The token comes from `PACK_STORE_TOKEN`, else
`VAULT_TOKEN`, never from the file.
