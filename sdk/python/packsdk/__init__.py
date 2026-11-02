"""packsdk: the author's side of pack. A pack is a dataclass; generate() writes its schema.json, pack.yaml and
templates/ — the artifacts the Go sdk writes, byte for byte the same format.

    from dataclasses import dataclass
    from packsdk import Pack, Meta, Root, Readiness, field, generate

    @dataclass
    class App(Pack):
        api_addr: str = field("", json="api_addr")
        def meta(self): return Meta(name="app", version="1.0.0", image="images/app:1.0.0")
        def pack_root(self): return Root(ports={"api": 8200}, readiness=Readiness(http="api/health"))

    generate("out", App())
"""

from ._fields import field
from ._pack import Pack, content, generate, main, pack_file, pack_file_yaml, packs, schema
from ._path import dotted, pointer
from ._reflect import SchemaError
from ._vocab import (File, Hook, Meta, OSPackages, Readiness, Rollout, Root, Rule, Secret, Step, Upgrade, Volume, When)

__all__ = ["Pack", "Meta", "Root", "File", "OSPackages", "Readiness", "Upgrade", "Hook", "Step", "When", "Rollout", "Rule", "Volume",
           "Secret", "SchemaError", "field", "pointer", "dotted", "generate", "schema", "pack_file", "pack_file_yaml",
           "packs", "content", "main"]
