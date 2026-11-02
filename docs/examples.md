# Examples

`examples/postgres` is one pack — a single-instance PostgreSQL with an install hook, a major-version upgrade hook with
its compensation, a formula, a `properties` file and two scripts shipped as they are — written three times, in Go,
Python and Java. All three generate the same artifacts, byte for byte, which `examples/postgres/golden` keeps and every
test compares with.

|                                                   |                                                                                                                                                                                                                                                        |
|---------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| [Go](../examples/postgres/go/postgres.go)         | `go run ./examples/postgres/go generate -o out` · `go test ./examples/postgres/go` writes the golden files (`UPDATE_GOLDEN=1`) and runs the author checks (`sdk.Verify`)                                                                               |
| [Python](../examples/postgres/python/postgres.py) | `python3 examples/postgres/python/postgres.py generate -o out` · `cd examples/postgres/python && python3 -m unittest test_postgres`                                                                                                                    |
| [Java](../examples/postgres/java/Postgres.java)   | from `examples/postgres/java`: `javac --release 21 -d out $(find ../../../sdk/java/src/main -name '*.java') Postgres.java PostgresTest.java`, then `java -cp out PostgresTest` (the test) or `java -Dpack.dir=. -cp out Postgres generate -o out/pack` |

What each shows, in the same order in all three:

- **settings** — `database` is required (no `omitempty`), `superuser.password` is a `Secret` read from the environment
  variable `POSTGRES_PASSWORD`, `conf` is written as `postgresql.conf` in the `properties` format;
- **defaults** — `SetDefaults` / `set_defaults` / `setDefaults`: what a zero value cannot say;
- **PackRoot** — the port, the data volume, the least it can run on, the readiness probe, the version it can be upgraded
  from, and three hooks: `init-extensions` (`postInstall`, idempotent), `pg-upgrade` (`preUpgrade` only from <16 to ≥16,
  exclusive, with a compensation that runs in the old image) and `analyze` (`postUpgrade`);
- **a formula** — `conf.shared_buffers` is a quarter of the memory limit unless the operator wrote it; its key is the
  field's pointer, found from the field itself ([authoring.md](authoring.md#formulas-and-rules));
- **files shipped as they are** — the scripts the hooks run are in `content/`, `format: raw`, mode 0755.

The generated directory is what `oci-packer` pushes ([registry.md](registry.md)). Packs that hold other packs are the
test packs of the core: `internal/testpacks/openbao` holds `internal/testpacks/tembo`, in Go, and the same two in
[Python](../sdk/python/tests/packs) and [Java](../sdk/java/src/test/java/io/horchestra/pack/sdk/packs).
