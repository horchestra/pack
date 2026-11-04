"""Reflect: a pack class into its JSON Schema, the way the Go sdk does it. The schema is the application's own
configuration: the attributes name the properties and a field without omitempty is required; the value after
set_defaults is the defaults document. A field whose type is a pack becomes a reference to its schema."""

from __future__ import annotations

import dataclasses
import types
import typing
from typing import Any
import re

ACTION_NAME = re.compile(r"^[a-z][a-z0-9-]*$")
PARAM_NAME = re.compile(r"^[a-z][a-z0-9_]*$")

from ._vocab import FORMATS, FORMAT_SECRET, PACK_SCHEME, PARAM_TYPES, Secret

SCHEMA = "https://json-schema.org/draft/2020-12/schema"



class SchemaError(Exception):
    pass


def tags(f: dataclasses.Field) -> dict:
    t = {"json": None, "env": None, "read_only": False,
         "omitempty": False, "skip": False, "enum": None, "pattern": None, "minimum": None, "maximum": None,
         "min_length": None, "max_length": None}
    t.update(f.metadata.get("pack", {}))
    return t


def json_name(f: dataclasses.Field) -> str | None:
    t = tags(f)
    if t["skip"] or t["json"] == "-":
        return None
    return t["json"] or f.name


def unwrap(tp: Any) -> Any:
    """T for Optional[T] and T | None."""
    origin = typing.get_origin(tp)
    if origin is typing.Union or origin is types.UnionType:
        args = [a for a in typing.get_args(tp) if a is not type(None)]
        if len(args) == 1:
            return unwrap(args[0])
    return tp


def is_pack(tp: Any) -> bool:
    from ._pack import Pack
    return isinstance(tp, type) and issubclass(tp, Pack)


def is_struct(tp: Any) -> bool:
    return isinstance(tp, type) and dataclasses.is_dataclass(tp) and tp is not Secret


def hints(cls: type) -> dict[str, Any]:
    return typing.get_type_hints(cls)


# ---- values

def is_zero(v: Any) -> bool:
    if v is None:
        return True
    if isinstance(v, (str, list, dict, tuple)):
        return len(v) == 0
    if isinstance(v, bool):
        return not v
    if isinstance(v, (int, float)):
        return v == 0
    if isinstance(v, Secret):
        return not (v.name or v.key)
    if dataclasses.is_dataclass(v):
        return all(is_zero(getattr(v, f.name)) for f in dataclasses.fields(v) if json_name(f))
    return False


def to_json(v: Any) -> Any:
    """encoding/json of a value: every field but the optional zero ones."""
    if isinstance(v, Secret):
        return v.to_json()
    if dataclasses.is_dataclass(v) and not isinstance(v, type):
        out = {}
        for f in dataclasses.fields(v):
            name = json_name(f)
            if name is None:
                continue
            fv = getattr(v, f.name)
            if tags(f)["omitempty"] and is_zero(fv):
                continue
            out[name] = to_json(fv)
        return out
    if isinstance(v, (list, tuple)):
        return [to_json(x) for x in v]
    if isinstance(v, dict):
        return {k: to_json(x) for k, x in v.items()}
    return v


def prune(v: Any) -> Any:
    """Drop zero values — "", 0, false, null, empty objects and arrays — from a marshalled value."""
    out, _ = _prune(to_json(v))
    return out


def _prune(v: Any):
    if isinstance(v, dict):
        out = {}
        for k, e in v.items():
            pe, keep = _prune(e)
            if keep:
                out[k] = pe
        return out, len(out) > 0
    if isinstance(v, list):
        return v, len(v) > 0
    if isinstance(v, str):
        return v, len(v) > 0
    if isinstance(v, bool):
        return v, v
    if isinstance(v, (int, float)):
        return v, v != 0
    if v is None:
        return None, False
    return v, True


def leaf_with_default(v: Any) -> bool:
    """A field set to something is a setting with that default; a struct is not one itself — its fields are —
    and neither is a secret."""
    if v is None or isinstance(v, Secret) or is_zero(v):
        return False
    return not (dataclasses.is_dataclass(v) and not isinstance(v, type))


# ---- types

def type_schema(tp: Any) -> dict:
    tp = unwrap(tp)
    if tp is Secret:
        return {"type": "string", "format": "password"}
    if tp is bool:
        return {"type": "boolean"}
    if tp is int:
        return {"type": "integer"}
    if tp is float:
        return {"type": "number"}
    if tp is str:
        return {"type": "string"}
    if tp is Any:
        return {}
    origin = typing.get_origin(tp)
    args = typing.get_args(tp)
    if origin in (list, tuple, set, frozenset):
        return {"items": type_schema(args[0]) if args else {}, "type": "array"}
    if origin is dict:
        value = args[1] if len(args) == 2 else Any
        return {"type": "object"} if value is Any else {"type": "object", "additionalProperties": type_schema(value)}
    if is_struct(tp):
        props: dict = {}
        required: list[str] = []
        h = hints(tp)
        for f in dataclasses.fields(tp):
            name = json_name(f)
            if name is None:
                continue
            props[name] = type_schema(h[f.name])
            if not tags(f)["omitempty"]:
                required.append(name)
        out = {"properties": props, "additionalProperties": False, "type": "object"}
        if required:
            out["required"] = required
        return out
    raise SchemaError(f"no JSON Schema for the type {tp!r}")




# ---- the schema

def reflect(p: Any) -> dict:
    """The schema of a pack: SetDefaults runs on the value first."""
    p.set_defaults()
    s: dict = {"$schema": SCHEMA}
    s.update(_struct(type(p), p, "", True))
    s["x-pack"] = p.pack_root().to_json()
    for ptr, expr in p.pack_compute().items():
        try:
            _lookup(s, ptr)["x-pack-compute"] = expr
        except SchemaError as e:
            raise SchemaError(f"x-pack-compute {ptr}: {e}") from None
    diags = p.pack_diagnose()
    seen: set[str] = set()
    for d in diags:
        if not d.id or not d.match or not d.cause:
            raise SchemaError(f"x-pack-diagnose: a diagnosis needs an id, a match and a cause: {d}")
        if d.id in seen:
            raise SchemaError(f'x-pack-diagnose: the id "{d.id}" twice')
        seen.add(d.id)
        for i, fx in enumerate(d.fix):
            if bool(fx.patch) == bool(fx.command) or (fx.patch and not fx.expr):
                raise SchemaError(f"x-pack-diagnose {d.id}: fix {i}: a patch with its expr, or a command")
            if fx.patch:
                try:
                    _lookup(s, fx.patch)
                except SchemaError as e:
                    raise SchemaError(f"x-pack-diagnose {d.id}: fix {i}: {fx.patch}: {e}") from None
    if diags:
        s["x-pack-diagnose"] = [d.to_json() for d in diags]
    actions = p.pack_actions()
    seen = set()
    for a in actions:
        if not ACTION_NAME.match(a.name):
            raise SchemaError(f'x-pack-actions: "{a.name}": a name of lower case letters, digits and dashes')
        if not a.command:
            raise SchemaError(f"x-pack-actions {a.name}: no command")
        if a.name in seen:
            raise SchemaError(f'x-pack-actions: the name "{a.name}" twice')
        seen.add(a.name)
        names = set()
        for pa in a.params:
            if not PARAM_NAME.match(pa.name):
                raise SchemaError(f'x-pack-actions {a.name}: parameter "{pa.name}": a name of lower case letters, digits and underscores')
            if pa.name in names:
                raise SchemaError(f'x-pack-actions {a.name}: parameter "{pa.name}" twice')
            names.add(pa.name)
            if pa.type not in PARAM_TYPES:
                raise SchemaError(f"x-pack-actions {a.name}: parameter {pa.name}: type is one of {list(PARAM_TYPES)}")
    if actions:
        s["x-pack-actions"] = [a.to_json() for a in actions]
    for ptr, sg in p.pack_suggest().items():
        if not sg.expr:
            raise SchemaError(f"x-pack-suggest {ptr}: no expression")
        try:
            _lookup(s, ptr)["x-pack-suggest"] = sg.to_json()
        except SchemaError as e:
            raise SchemaError(f"x-pack-suggest {ptr}: {e}") from None
    ids: dict[str, str] = {}
    for ptr, rules in p.pack_rules().items():
        for r in rules:
            if r.id and r.id in ids:
                raise SchemaError(f'x-pack-rules: the id "{r.id}" is of a rule on {ids[r.id]} and on {ptr}')
            if r.id:
                ids[r.id] = ptr
        try:
            _lookup(s, ptr)["x-pack-rules"] = [r.to_json() for r in rules]
        except SchemaError as e:
            raise SchemaError(f"x-pack-rules {ptr}: {e}") from None
    return s


def _struct(tp: type, value: Any, path: str, root: bool) -> dict:
    out = type_schema(tp)
    _annotate(out, tp, value, path)
    return out


def _annotate(node: dict, tp: type, value: Any, path: str) -> None:
    """Defaults, files and environment variables onto the node of a struct. A struct behind a pointer that is not set
    (value is None) has none of them, as in the Go sdk."""
    if value is None:
        return
    h = hints(tp)
    for f in dataclasses.fields(tp):
        name = json_name(f)
        if name is None:
            continue
        core = unwrap(h[f.name])
        fv = getattr(value, f.name)
        if is_pack(core):
            if path:
                raise SchemaError(f"{path}/{name}: a pack is held by a field of the pack itself, not of a struct inside it")
            ref = {"$ref": f"{PACK_SCHEME}{core().meta().name}#"}
            if fv is not None and not is_zero(fv):
                ref["default"] = prune(fv)
            node["properties"][name] = ref
            continue
        prop = node["properties"][name]
        if leaf_with_default(fv):
            prop["default"] = to_json(fv)
        if tags(f)["read_only"]:
            prop["readOnly"] = True
        _constrain(prop, tags(f), f"{path}/{name}")
        env = tags(f)["env"]
        if env and env != "-":
            prop["x-pack-env"] = env
        if is_struct(core):
            _annotate(prop, core, fv, f"{path}/{name}")


# what a value may be: the field's argument, and the keyword of the schema it is
_CONSTRAINTS = (("enum", "enum"), ("pattern", "pattern"), ("minimum", "minimum"), ("maximum", "maximum"),
                ("min_length", "minLength"), ("max_length", "maxLength"))


# the schema types each constraint is of
_OF = {"enum": ("string", "integer", "number"), "pattern": ("string",), "min_length": ("string",),
       "max_length": ("string",), "minimum": ("integer", "number"), "maximum": ("integer", "number")}


def _is(v: Any, typ: str) -> bool:
    if isinstance(v, bool):
        return typ == "boolean"
    return {"string": isinstance(v, str), "integer": isinstance(v, int) or (isinstance(v, float) and v.is_integer()),
            "number": isinstance(v, (int, float))}.get(typ, False)


def _constrain(prop: dict, t: dict, where: str) -> None:
    """What a value of the setting may be, onto its node: a constraint its type has not, or an enum value of another
    type, is refused (a schema no value passes), as the Java sdk refuses it."""
    typ = prop.get("type")
    for arg, key in _CONSTRAINTS:
        v = t[arg]
        if v is None:
            continue
        if typ not in _OF[arg] or prop.get("format") == "password":
            raise SchemaError(f"{where}: {arg} is of {' or '.join(_OF[arg])}, not of this {typ}")
        if arg == "enum":
            bad = [x for x in v if not _is(x, typ)]
            if bad:
                raise SchemaError(f"{where}: enum {bad!r} is not of type {typ}")
            v = list(v)
        elif arg in ("min_length", "max_length") and (isinstance(v, bool) or not isinstance(v, int) or v < 0):
            raise SchemaError(f"{where}: {arg} {v!r} is not a length")
        elif arg in ("minimum", "maximum") and not _is(v, "number"):
            raise SchemaError(f"{where}: {arg} {v!r} is not a number")
        prop[key] = v


def _lookup(s: dict, ptr: str) -> dict:
    node = s
    for seg in ptr.removeprefix("/").split("/"):
        props = node.get("properties")
        if props is None:
            raise SchemaError(f'"{seg}": not an object')
        if seg not in props:
            raise SchemaError(f'"{seg}": no such property')
        node = props[seg]
    return node
