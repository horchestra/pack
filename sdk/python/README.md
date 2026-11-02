# packsdk — the author's side of pack, in Python

A pack is a dataclass; `generate()` writes its `schema.json`, `pack.yaml` and `templates/` — the artifacts the Go sdk
writes (`sdk/`), byte for byte the same. The author checks run on the result: `pack check <dir>`.

```python
from dataclasses import dataclass
from typing import Optional
from packsdk import Meta, Pack, Readiness, Root, Rule, dotted, field, generate, pointer

@dataclass
class Config:
    api_addr: str = field("")                          # required: no omitempty
    log_level: str = field("", omitempty=True)

@dataclass
class OpenBao(Pack):
    bao: Config = field(factory=Config, json="openbao", file="/etc/openbao/config.json")
    postgres: Optional[Tembo] = field(None, omitempty=True)    # a pack of its own; Optional makes it optional

    def set_defaults(self):  self.bao.log_level = "info"
    def meta(self):          return Meta(name="openbao", version="2.1.0", image="oci://images/openbao:2.1.0",
                                         artifact="oci://packs/openbao")
    def pack_root(self):     return Root(ports={"api": 8200}, readiness=Readiness(http="api/v1/sys/health"),
                                         savepoints=[dotted(self, lambda o: o.postgres)])
    def pack_compute(self):  return {pointer(self, lambda o: o.bao.storage): "…CEL…"}

generate("out", OpenBao())     # out/openbao/…, out/tembo/…
```

- `field()` takes the Go tags as arguments: `json`, `env`, `file`, `template`, `format`, `mode`, `parse_as`,
  `omitempty` (without it the field is required), `skip`. The default is the zero value; `set_defaults` sets the rest.
- The files a pack ships are in `content/` next to the class's source; `template=` names one.
- **The names of fields in formulas and rules are not written by hand**: `pointer(self, lambda o: o.bao.storage)` is
  `/openbao/storage` (the key of `pack_compute`/`pack_rules`), `dotted(self, lambda o: o.postgres)` is `postgres`
  (`pack.values.postgres`, `pack.keys.postgres.service`). A field that is not there is a `SchemaError`.
- `packsdk.main(*roots)` is a ready CLI: `generate [-o dir]`, `schema <name>`.

Tests: `python3 -I -m unittest discover -s tests` — the generated artifacts of the test packs equal
`sdk/testdata/golden` (written by the Go sdk) byte for byte.
