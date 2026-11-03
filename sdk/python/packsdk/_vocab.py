"""The vocabulary a pack speaks on top of JSON Schema: x-pack (Root, its Content of File), x-pack-rules (Rule), and
Meta, everything about a pack that goes into its OCI artifact. The same words as the Go sdk, one to one."""

from __future__ import annotations

from dataclasses import dataclass, field as _field

PHASES = ("preInstall", "postInstall", "preUpgrade", "postUpgrade", "preDowngrade", "postDowngrade")
RULE_PHASES = ("preMutate", "postMutate")
FORMATS = ("json", "yaml", "properties", "env", "shell", "ini", "flagfile", "lines", "hadoop-xml", "toml", "text")
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
class File:
    """A file in the component's containers, at path: from source (a file of content/, byte for byte), template (a file
    of content/ rendered over the values the pack sees; with values, only when that value is there), or values (a
    field's subtree, "conf", written in format, the name of path saying it when there is none)."""
    path: str
    source: str = ""
    template: str = ""
    url: str = ""
    values: str = ""
    format: str = ""
    parse_as: str = ""
    mode: str = ""
    per_instance: bool = False

    def to_json(self) -> dict:
        return _drop({"path": self.path, "source": self.source, "template": self.template, "url": self.url, "values": self.values,
                      "format": self.format, "parseAs": self.parse_as, "mode": self.mode,
                      "perInstance": self.per_instance})


@dataclass
class OSPackages:
    """The installation on one OS, in this order: prepare installed, repo delivered, names installed, commands run,
    content delivered, services started. A name or the url of a file is a template too; the target resolves
    ${os:codename}, ${os:version} and ${os:arch}."""
    names: list[str]
    prepare: list[str] = _field(default_factory=list)
    repo: list[File] = _field(default_factory=list)
    commands: list[list[str]] = _field(default_factory=list)
    content: list[File] = _field(default_factory=list)
    services: list[str] = _field(default_factory=list)

    def to_json(self) -> dict:
        out: dict = {}
        if self.prepare:
            out["prepare"] = list(self.prepare)
        if self.repo:
            out["repo"] = [f.to_json() for f in self.repo]
        out["names"] = list(self.names)
        if self.commands:
            out["commands"] = [list(c) for c in self.commands]
        if self.content:
            out["content"] = [f.to_json() for f in self.content]
        if self.services:
            out["services"] = list(self.services)
        return out


@dataclass
class Root:
    """x-pack on the root of a pack's schema: what the component is to the system around it."""
    ports: dict[str, int] = _field(default_factory=dict)
    data: dict[str, Volume] = _field(default_factory=dict)
    content: list[File] = _field(default_factory=list)
    packages: dict[str, OSPackages] = _field(default_factory=dict)  # by OS: the key is the target's to match
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
        if self.content:
            out["content"] = [f.to_json() for f in self.content]
        if self.packages:
            out["packages"] = {k: self.packages[k].to_json() for k in sorted(self.packages)}
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
class Fix:
    """A way out of a failure: a value for a setting (patch, its pointer, and expr, CEL) or a command to run."""
    patch: str = ""
    expr: str = ""
    command: str = ""
    why: str = ""

    def to_json(self) -> dict:
        return _drop({"patch": self.patch, "expr": self.expr, "command": self.command, "why": self.why})


@dataclass
class Diagnosis:
    """One of x-pack-diagnose on the root: what a failure looks like (match, RE2 over the step's error and logs), what
    it is (cause), the ways out (fix)."""
    id: str
    match: str
    cause: str
    fix: list[Fix] = _field(default_factory=list)

    def to_json(self) -> dict:
        return {"id": self.id, "match": self.match, "cause": self.cause, **_drop({"fix": [f.to_json() for f in self.fix]})}


PARAM_TYPES = ("string", "integer", "number", "boolean")
ACTION_ORDER = ("name", "description", "command", "timeout", "mutating", "approve", "params", "x-pack-i18n")
PARAM_ORDER = ("type", "description", "default", "enum", "x-pack-i18n")


def ordered(d: dict, order) -> dict:
    """d with its keys in the order the Go sdk writes them."""
    return {k: d[k] for k in order if k in d}


@dataclass
class Param:
    """A parameter of an action: a scalar of type (string, integer, number, boolean)."""
    name: str
    type: str
    required: bool = False
    description: str = ""
    default: object = None
    enum: list = _field(default_factory=list)

    def to_json(self) -> dict:
        out = {"type": self.type, **_drop({"description": self.description})}
        if self.default is not None:
            out["default"] = self.default
        return {**out, **_drop({"enum": list(self.enum)})}


@dataclass
class Action:
    """One of x-pack-actions on the root: an operation the author names, pack run performs in the first instance of a key
    of the pack, as it runs. command's words are templates over the parameters ({{ .to }}); mutating — it changes the
    installation; approve — never run unasked but under run.approve: auto."""
    name: str
    command: list[str]
    description: str = ""
    timeout: str = ""
    mutating: bool = False
    approve: bool = False
    params: list[Param] = _field(default_factory=list)

    def to_json(self) -> dict:
        out = {"name": self.name, **_drop({"description": self.description}), "command": list(self.command),
               **_drop({"timeout": self.timeout, "mutating": self.mutating, "approve": self.approve})}
        if self.params:
            ps = {"type": "object", "properties": {p.name: p.to_json() for p in sorted(self.params, key=lambda p: p.name)}}
            req = sorted(p.name for p in self.params if p.required)
            if req:
                ps["required"] = req
            ps["additionalProperties"] = False
            out["params"] = ps
        return out


@dataclass
class Suggest:
    """x-pack-suggest on a node: a CEL expression pack fill offers the answer of for a setting not given, with why —
    never a value by itself, as a formula's."""
    expr: str
    why: str = ""

    def to_json(self) -> dict:
        return {"expr": self.expr, **_drop({"why": self.why})}


@dataclass
class Rule:
    """x-pack-rules on a node: a CEL expression that must hold of the values. id names it among the pack's rules, so
    that docs/<lang>.yaml can say its message in other languages (rules: {<id>: …})."""
    rule: str
    message: str = ""
    phases: list[str] = _field(default_factory=list)
    id: str = ""

    def to_json(self) -> dict:
        return {**_drop({"id": self.id}), "rule": self.rule, **_drop({"message": self.message, "phases": self.phases})}


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
    image: str = ""  # repo:tag the application runs, a repository of the registry the pack is pushed to; "" — the pack runs nothing itself
    annotations: dict[str, str] = _field(default_factory=dict)
    dir: str = ""  # the pack's directory, whose content/ holds its files; "" — where the class is defined


@dataclass
class Secret:
    """A credential leaf: the operator gives a reference to a Secret, never a value."""
    name: str = ""
    key: str = ""

    def to_json(self) -> dict:
        return {"secretRef": {"name": self.name, "key": self.key}} if self.name or self.key else {}
