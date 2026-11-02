# pack-sdk — the author's side of pack, in Java

A pack is a class extending `Pack`; `PackSdk.generate` writes its `schema.json`, `pack.yaml` and `templates/` — the
artifacts the Go sdk writes (`sdk/`), byte for byte the same. No dependencies (JSON and the little YAML are written
here), Java 21. The author checks run on the result: `pack check <dir>`.

```java
public class OpenBao extends Pack {
    public static class Config {
        @Setting(json = "api_addr") public String apiAddr = "";                 // required: no omitempty
        @Setting(json = "log_level", omitempty = true) public String logLevel = "";
    }
    @Setting(json = "openbao", file = "/etc/openbao/config.json") public Config bao = new Config();
    @Setting(omitempty = true) public Tembo postgres;                            // a pack of its own; optional: null

    @Override public void setDefaults() { bao.logLevel = "info"; }
    @Override public Meta meta() { return new Meta("openbao", "2.1.0").image("oci://images/openbao:2.1.0").artifact("oci://packs/openbao"); }
    @Override public Root packRoot() {
        return new Root().port("api", 8200).readiness(Root.Readiness.http("api/v1/sys/health"))
                .savepoint(Paths.dotted(OpenBao.class, "postgres"));
    }
    @Override public Map<String, String> packCompute() {
        return Map.of(Paths.pointer(OpenBao.class, "bao", "storage"), "…CEL…");
    }
}

PackSdk.generate(Path.of("out"), new OpenBao());   // out/openbao/…, out/tembo/…
// or: System.exit(PackSdk.main(args, new OpenBao()));   generate [-o dir] | schema <name>
```

- `@Setting` takes the Go tags: `json`, `env`, `file`, `template`, `format`, `mode`, `parseAs`, `omitempty` (without it
  the field is required), `skip`. A pack class needs a no-argument constructor; `setDefaults` sets the defaults.
- The files a pack ships are in `content/` under `Meta.dir(…)` (the working directory by default); `template` names one.
- **The names of fields in formulas and rules are not written by hand**: `Paths.pointer(OpenBao.class, "bao", "storage")`
  is `/openbao/storage`, `Paths.dotted(OpenBao.class, "postgres")` is `postgres`. The arguments are the Java fields'
  names, the result is the schema's; a field that is not there is a `SchemaException`.

Tests (no framework; a JDK is in GoLand's `jbr`, `Contents/Home/bin`), from `sdk/java`:

```
javac --release 21 -d out/main $(find src/main -name '*.java')
javac --release 21 -cp out/main -d out/test $(find src/test -name '*.java')
java -cp out/main:out/test dev.kstool.pack.sdk.GoldenTest
```

The generated artifacts of the test packs equal `sdk/testdata/golden` (written by the Go sdk) byte for byte.
