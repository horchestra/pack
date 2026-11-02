"""The names of a pack's fields in formulas and rules, found from the fields themselves: the author writes none of
them by hand, so a renamed json name or a moved field moves them along.

    pointer(self, lambda o: o.bao.storage)   ->  "/openbao/storage"   the key of pack_compute / pack_rules
    dotted(self, lambda o: o.postgres)       ->  "postgres"            as a formula reads it: pack.values.postgres
"""

from __future__ import annotations

import dataclasses
import re
from typing import Any, Callable

from ._reflect import SchemaError, hints, is_struct, json_name, unwrap, is_pack

_IDENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class _Trace:
    def __init__(self, tp: Any, names: list[str]):
        object.__setattr__(self, "_tp", tp)
        object.__setattr__(self, "_names", names)

    def __getattr__(self, attr: str) -> "_Trace":
        tp = unwrap(self._tp)
        if not (is_struct(tp) or is_pack(tp)):
            raise SchemaError(f"{'.'.join(self._names) or 'the pack'} has no fields: {attr!r}")
        for f in dataclasses.fields(tp):
            if f.name == attr:
                name = json_name(f)
                if name is None:
                    raise SchemaError(f"{attr!r} is not in the schema (skipped)")
                return _Trace(hints(tp)[attr], [*self._names, name])
        raise SchemaError(f"{tp.__name__} has no field {attr!r}")


def _names(root: Any, select: Callable[[Any], Any]) -> list[str]:
    tp = root if isinstance(root, type) else type(root)
    end = select(_Trace(tp, []))
    if not isinstance(end, _Trace) or not end._names:
        raise SchemaError("select a field of the pack: lambda o: o.some.field")
    return end._names


def pointer(root: Any, select: Callable[[Any], Any]) -> str:
    """The JSON Pointer of a field of root."""
    return "".join("/" + n.replace("~", "~0").replace("/", "~1") for n in _names(root, select))


def dotted(root: Any, select: Callable[[Any], Any]) -> str:
    """The path of a field the way a formula or a rule reads it; a name a formula cannot select is refused."""
    names = _names(root, select)
    for n in names:
        if not _IDENT.match(n):
            raise SchemaError(f"{n!r} is not a name a formula can select")
    return ".".join(names)
