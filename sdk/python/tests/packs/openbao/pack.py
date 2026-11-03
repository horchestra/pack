"""openbao, which holds tembo, the pack of the Go tests (internal/testpacks/openbao), written in Python."""

from dataclasses import dataclass
from typing import Any, Optional

from packsdk import File, Meta, Pack, Readiness, Root, Rule, dotted, field, pointer

from ..tembo.pack import Tembo


@dataclass
class StorageBlock:
    type: str = field("")
    config: dict[str, Any] = field(factory=dict, omitempty=True)


@dataclass
class Config:
    ui: bool = field(False, omitempty=True)
    log_level: str = field("", omitempty=True)
    api_addr: str = field("")
    storage: list[StorageBlock] = field(factory=list, omitempty=True)


VERSION = "2.1.0"
IMAGE = "images/openbao:2.1.0"


@dataclass
class OpenBao(Pack):
    bao: Config = field(factory=Config, json="openbao")
    postgres: Optional[Tembo] = field(None, omitempty=True)

    def set_defaults(self):
        self.bao.log_level = "info"

    # The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
    # postgres and wrote no storage himself.
    def pack_compute(self):
        pg = dotted(self, lambda o: o.postgres)
        return {pointer(self, lambda o: o.bao.storage): (
            f"has(pack.values.{pg}) ? [{{'type': 'postgresql', 'config': {{'connection_url': "
            f"'postgres://' + pack.keys.{pg}.service + ':5432/openbao'}}}}] : null")}

    # ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's.
    def pack_rules(self):
        pg, storage = dotted(self, lambda o: o.postgres), dotted(self, lambda o: o.bao.storage)
        return {pointer(self, lambda o: o.bao.storage): [Rule(
            id="storage-from-postgres",
            rule=f"!has(pack.values.{pg}) || !has(values.{storage}) || size(values.{storage}) == 0",
            message="storage is filled from the embedded postgres and must not be set")]}

    def meta(self):
        return Meta(name="openbao", version=VERSION, description="secrets", license="MPL-2.0", image=IMAGE, annotations={"example.org/tier": "core"})

    def pack_root(self):
        return Root(ports={"api": 8200}, content=[File(path="/etc/openbao/config.json", values="openbao")], readiness=Readiness(http="api/v1/sys/health"),
                    savepoints=[dotted(self, lambda o: o.postgres)])
