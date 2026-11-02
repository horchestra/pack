"""Reflect: a pack class into its JSON Schema, the way the Go sdk does it. The schema is the application's own
configuration: the attributes name the properties and a field without omitempty is required; the value after
set_defaults is the defaults document. A field whose type is a pack becomes a reference to its schema."""

from __future__ import annotations

import dataclasses
import types
import typing
from typing import Any

from ._vocab import FORMATS, FORMAT_SECRET, PACK_SCHEME, Secret

SCHEMA = "https://json-schema.org/draft/2020-12/schema"

_EXT = {".json": "json", ".yaml": "yaml", ".yml": "yaml", ".properties": "properties", ".toml": "toml",
        ".ini": "ini", ".cnf": "ini", ".env": "env"}


class SchemaError(Exception):
    pass


def tags(f: dataclasses.Field) -> dict:
    t = {"json": None, "env": None, "file": None, "template": None, "format": None, "mode": None, "parse_as": None,
         "omitempty": False, "skip": False}
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


# ---- files

def file_of(f: dataclasses.Field) -> dict | None:
    t = tags(f)
    dst = t["file"]
    if not dst:
        for k in ("template", "format", "mode", "parse_as"):
            if t[k]:
                raise SchemaError(f"{f.name}: a {k} argument, but no file to say where")
        return None
    file = {"path": dst, "format": t["format"] or "", "template": t["template"] or "", "parseAs": t["parse_as"] or "",
            "mode": t["mode"] or ""}
    if not file["format"] and file["template"]:
        file["format"] = "template"
    elif not file["format"]:
        file["format"] = _EXT.get(_ext(dst).lower(), "")
    if not file["format"]:
        raise SchemaError(f"{f.name}: format of {dst} is not known by its name: say it with the format argument")
    if file["format"] not in FORMATS and file["format"] != FORMAT_SECRET:
        raise SchemaError(f'{f.name}: format "{file["format"]}" is not one of {list(FORMATS)}')
    fmt, tmpl = file["format"], file["template"]
    if fmt == "template":
        if not tmpl.endswith(".ptmpl"):
            raise SchemaError(f'{f.name}: a rendered file (format template) is a *.ptmpl file of content/: "{tmpl}"')
    elif fmt == "raw":
        if not tmpl or tmpl.endswith(".ptmpl"):
            raise SchemaError(f'{f.name}: a file shipped as it is (format raw) names a content/ file that is not *.ptmpl: "{tmpl}"')
    elif tmpl:
        raise SchemaError(f'{f.name}: template "{tmpl}", but the format is {fmt}: only template and raw files come from content/')
    return {k: v for k, v in file.items() if v}


def _ext(path: str) -> str:
    base = path.rsplit("/", 1)[-1]
    i = base.rfind(".")
    return base[i:] if i > 0 else ""


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
    for ptr, rules in p.pack_rules().items():
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
        try:
            file = file_of(f)
        except SchemaError as e:
            raise SchemaError(f"{path}/{name}: {e}") from None
        if file:
            prop["x-pack-file"] = file
        env = tags(f)["env"]
        if env and env != "-":
            prop["x-pack-env"] = env
        if is_struct(core):
            _annotate(prop, core, fv, f"{path}/{name}")


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
