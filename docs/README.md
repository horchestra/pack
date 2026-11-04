# pack — documentation

|                              |                                                                                                                          |
|------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| [authoring.md](authoring.md) | writing a pack: the struct, `Meta`, `PackRoot`, formulas and rules, files, packs that hold packs; in Go, Python and Java |
| [values.md](values.md)       | the values document, the effective document, the CEL environment, the credentials pack creates                           |
| [emitters.md](emitters.md)   | the formats a file can be written in, the template language                                                              |
| [release.md](release.md)     | the directory `pack` runs in: `release.yaml`, values, `env.yaml`, the lock, the state                                    |
| [commands.md](commands.md)   | every command and flag                                                                                                   |
| [targets.md](targets.md)     | the targets (docker, ansible, k8s): programs of their own, how pack starts them, how to write one, the protocol          |
| [examples.md](examples.md)   | one pack, written in Go, Python and Java                                                                                 |
| [registry.md](registry.md)   | publishing a pack to a registry, `pack init`, `pack refresh`, the lock, `.pack/`                                         |
| [saga.md](saga.md)           | what `plan` and `apply` do: the plan, savepoints, rollback, drift, the journal                                           |
