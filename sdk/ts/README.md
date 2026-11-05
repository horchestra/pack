# packsdk — the author's side of pack, in TypeScript

A pack is a class whose fields, declared with `field()`, are the application's settings; `generate()` writes its
`schema.json`, `pack.yaml` and `templates/` — the artifacts the Go sdk writes (`sdk/`), byte for byte the same. The
author checks run on the result: `pack check <dir>`.

No dependencies and no build step: node ≥ 22.6 runs the TypeScript directly (type stripping; the code is
erasable-syntax only, checked with `tsc --noEmit`).

```ts
import { dotted, field, generate, Pack, pointer, Secret } from "packsdk";
import type { Meta, Root } from "packsdk";
import type { Tembo } from "./tembo/pack.ts";

class Config {
  apiAddr = field("", { json: "api_addr" });      // required: no omitempty
  logLevel = field("", { json: "log_level", omitempty: true });
}

class OpenBao extends Pack {
  bao = field(new Config(), { json: "openbao" });
  postgres = field(null as Tembo | null, { pack: Tembo, omitempty: true }); // a pack of its own; null makes it optional

  setDefaults() { this.bao.logLevel = "info"; }
  meta(): Meta { return { name: "openbao", version: "2.1.0", image: "images/openbao:2.1.0" }; }
  packRoot(): Root {
    return { ports: { api: 8200 }, readiness: { http: "api/v1/sys/health" },
             savepoints: [dotted(this, (o) => o.postgres)] };
  }
  packCompute() { return { [pointer(this, (o) => o.bao.storage)]: "…CEL…" }; }
}

generate("out", new OpenBao());     // out/openbao/…, out/tembo/…
```

- `field()` takes the Go tags as options: `json` (the property's name — the class field's name by default, so a
  snake_case key of the application is named in `json`), `env`, `readOnly`, `omitempty` (without it the field is
  required), `skip`, and what a value may be — `enum`, `pattern`, `minimum`, `maximum`, `minLength`, `maxLength`.
  The default is the zero value; a function default is its factory (`field(() => new Config())`);
  `setDefaults()` sets the rest.
- The schema type is the default's: `""` a string, `false` a boolean, `0` an integer (`number: true` when it is a
  number that happens to be whole), `[]` an array (`of:` names the item — a scalar's name or a struct class), `{}` a
  map (`values:` its value type), a `Secret` a password, an instance of a class a nested object.
- The files a pack ships are in `content/` next to the pack's source; `packRoot(): { content: [{ path, … }] }` says
  which go where. The directory is found from the class's own source file; `Meta.dir` says it by hand when that is
  not where the files are.
- The documentation of the settings is in `docs/<lang>.yaml` beside `content/`, read here without a library the way
  the Go sdk reads it ([../../docs/authoring.md](../../docs/authoring.md#documentation)); `Rule`'s `id` names a rule
  whose message it translates.
- **The names of fields in formulas and rules are not written by hand**: `pointer(this, (o) => o.bao.storage)` is
  `/openbao/storage` (the key of `packCompute`/`packRules`), `dotted(this, (o) => o.postgres)` is `postgres`
  (`pack.values.postgres`, `pack.keys.postgres.service`). A field that is not there is a `SchemaError`.
- `main(...roots)` is a ready CLI: `generate [-o dir] [--repo packs]`, `schema <name>`.

Tests: `node --test tests/` — the generated artifacts of the test packs equal `sdk/internal/testdata/golden`
(written by the Go sdk) byte for byte.
