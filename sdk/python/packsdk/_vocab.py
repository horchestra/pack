"""The vocabulary a pack speaks on top of JSON Schema: x-pack (Root), x-pack-file (File), x-pack-rules (Rule), and
Meta, everything about a pack that goes into its OCI artifact. The same words as the Go sdk, one to one."""

from __future__ import annotations

from dataclasses import dataclass, field as _field

PHASES = ("preInstall", "postInstall", "preUpgrade", "postUpgrade", "preDowngrade", "postDowngrade")
RULE_PHASES = ("preMutate", "postMutate")
FORMATS = ("json", "yaml", "properties", "env", "shell", "ini", "flagfile", "lines", "hadoop-xml", "toml", "text",
           "template", "raw")
FORMAT_SECRET = "secret"
PACK_SCHEME = "pack://"


def _drop(d: dict) -> dict:
    """omitempty: what is empty is not written."""
    return {k: v for k, v in d.items() if v not in (None, "", 0, False, [], {})}


@dataclass
class Volume:
    """A volume the component keeps: where it is mounted is the component's fact, how big is the operator's."""
    mount_path: str
    size: str = ""

    def to_json(self) -> dict:
        return {"mountPath": self.mount_path, **_drop({"size": self.size})}


@dataclass
class Readiness:
    tcp: str = ""   # a port name
    http: str = ""  # "<port>/<path>"
    exec: list[str] = _field(default_factory=list)

    def to_json(self) -> dict:
        return _drop({"tcp": self.tcp, "http": self.http, "exec": self.exec})


@dataclass
class Upgrade:
    from_: str  # version constraint on the version being upgraded from

    def to_json(self) -> dict:
        return {"from": self.from_}


@dataclass
class When:
    from_: str = ""
    to: str = ""

    def to_json(self) -> dict:
        return _drop({"from": self.from_, "to": self.to})


@dataclass
class Step:
    command: list[str]
    image: str = ""  # new | old

    def to_json(self) -> dict:
        return {**_drop({"image": self.image}), "command": list(self.command)}


@dataclass
class Hook:
    """One step the component contributes to the saga of its own installation or upgrade."""
    name: str
    phase: str
    command: list[str]
    when: When | None = None
    image: str = ""
    exclusive: bool = False
    timeout: str = ""
    idempotent: bool = False
    compensate: Step | None = None

    def to_json(self) -> dict:
        out = {"name": self.name, "phase": self.phase}
        if self.when is not None:
            out["when"] = self.when.to_json()
        out.update(_drop({"image": self.image}))
        out["command"] = list(self.command)
        out.update(_drop({"exclusive": self.exclusive, "timeout": self.timeout, "idempotent": self.idempotent}))
        if self.compensate is not None:
            out["compensate"] = self.compensate.to_json()
        return out


@dataclass
class Rollout:
    max_unavailable: int = 0

    def to_json(self) -> dict:
        return _drop({"maxUnavailable": self.max_unavailable})


@dataclass
class Root:
    """x-pack on the root of a pack's schema: what the component is to the system around it."""
    ports: dict[str, int] = _field(default_factory=dict)
    data: dict[str, Volume] = _field(default_factory=dict)
    minimum: dict[str, str] = _field(default_factory=dict)
    readiness: Readiness | None = None
    upgrade: Upgrade | None = None
    hooks: list[Hook] = _field(default_factory=list)
    command: list[str] = _field(default_factory=list)  # replaces the image's entrypoint+cmd wholesale
    restart_policy: str = ""  # Always | OnFailure | Never
    savepoints: list[str] = _field(default_factory=list)
    rollout: Rollout | None = None

    def to_json(self) -> dict:
        out: dict = {}
        if self.ports:
            out["ports"] = dict(self.ports)
        if self.data:
            out["data"] = {k: v.to_json() for k, v in self.data.items()}
        if self.minimum:
            out["minimum"] = dict(self.minimum)
        if self.readiness is not None:
            out["readiness"] = self.readiness.to_json()
        if self.upgrade is not None:
            out["upgrade"] = self.upgrade.to_json()
        if self.hooks:
            out["hooks"] = [h.to_json() for h in self.hooks]
        if self.command:
            out["command"] = list(self.command)
        if self.restart_policy:
            out["restartPolicy"] = self.restart_policy
        if self.savepoints:
            out["savepoints"] = list(self.savepoints)
        if self.rollout is not None:
            out["rollout"] = self.rollout.to_json()
        return out


@dataclass
class Rule:
    """x-pack-rules on a node: a CEL expression that must hold of the values."""
    rule: str
    message: str = ""
    phases: list[str] = _field(default_factory=list)

    def to_json(self) -> dict:
        return {"rule": self.rule, **_drop({"message": self.message, "phases": self.phases})}


@dataclass
class Meta:
    """Everything about a pack that goes into its OCI artifact. name and version are required."""
    name: str  # a DNS label; the key of the artifact among the packs of a pack
    version: str
    description: str = ""
    documentation: str = ""
    source: str = ""
    url: str = ""
    license: str = ""
    vendor: str = ""
    authors: str = ""
    created: str = ""  # RFC 3339; empty — not written, so that the artifact is the same byte for byte
    image: str = ""  # oci://repo:tag the application runs; "" — the pack runs nothing itself
    artifact: str = ""  # oci://repo where the pack is published: what a pack that holds it mounts it from
    annotations: dict[str, str] = _field(default_factory=dict)
    dir: str = ""  # the pack's directory, whose content/ holds its files; "" — where the class is defined


@dataclass
class Secret:
    """A credential leaf: the operator gives a reference to a Secret, never a value."""
    name: str = ""
    key: str = ""

    def to_json(self) -> dict:
        return {"secretRef": {"name": self.name, "key": self.key}} if self.name or self.key else {}
