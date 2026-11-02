# Writing a pack

A pack is **an application's configuration as a type**, plus a few methods that say how the application runs. The
same pack can be written in Go (`sdk/`), Python (`sdk/python`) or Java (`sdk/java`); all three write the same
artifacts, byte for byte (`sdk/testdata/golden` is the proof the tests compare with).

| method          |                                                                                                                                         |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------|
| `SetDefaults()` | what a zero value cannot say. It sets, never adds: it may run twice. A field that is not zero afterwards is a setting with that default |
| `Meta()`        | the identity: `Name`, `Version` (required), `Description`, `License`, `Source`, `Image`, `Artifact`, `Annotations` …                    |
| `PackRoot()`    | how it runs, upgrades and rolls back (below)                                                                                            |
| `PackCompute()` | optional: `{pointer: CEL formula}` that fills a value the operator did not write                                                        |
| `PackRules()`   | optional: `{pointer: [rule, …]}` that the values must satisfy                                                                           |

```go
type OpenBao struct {
Bao      Config        `json:"openbao" file:"/etc/openbao/config.json"`
Postgres *tembo.Config `json:"postgres,omitempty"` // a pack of its own; a pointer makes it optional
}
```

## The struct

The fields are the application's own settings: the `json` tag is the key in the schema (use the application's own
name), **a field without `omitempty` is required**. Other tags:

| tag                  |                                                                                                                                                                                                                        |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `env:"NAME"`         | the field is the environment variable `NAME`                                                                                                                                                                           |
| `file:"/path"`       | the field — a leaf or a whole subtree — becomes the file `/path` in the container                                                                                                                                      |
| `template:"x.ptmpl"` | …rendered from `content/x.ptmpl`, a template over the values; with `format:"raw"` the file `content/x` is shipped as it is                                                                                             |
| `format:"json"`      | …or the subtree written in that format: json, yaml, properties, env, ini, toml, shell, flagfile, lines, hadoop-xml, text; `secret` mounts a secret's key untouched. The name's extension says it when the tag does not |
| `mode:"0640"`        | the file's mode                                                                                                                                                                                                        |
| `parseAs:"text"`     | how a template's output is parsed back                                                                                                                                                                                 |

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

A pack's files are in `content/`, next to the source (Go: the file that defines `Meta`; Python: the class's file; Java:
`Meta.dir`). They are read from disk, never embedded. Every file a `template` tag names must be there; the files go into
the artifact as `templates/`.

## A pack that holds a pack

A field whose type is a pack holds it: the settings of the two join in one schema (the held one as `pack://name#`), a
held pack is installed **before** the pack that holds it, and the held pack's `Meta` needs an `Artifact`
(`oci://repo`) for the holder to mount it from. A pointer (Go), `Optional` (Python) or `null` (Java) makes it optional.
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
