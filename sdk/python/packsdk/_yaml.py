"""The little YAML pack.yaml needs: maps (keys sorted), lists, strings."""

from __future__ import annotations

import json
import re
from typing import Any

_PLAIN = re.compile(r"^[A-Za-z0-9_./@+][A-Za-z0-9_./:@+ -]*$")
_RESERVED = re.compile(r"^(true|false|null|yes|no|on|off|y|n|~)$", re.I)
_NUMBER = re.compile(r"^[-+]?(\d[\d_]*)?\.?\d*([eE][-+]?\d+)?$|^0[xob][0-9a-fA-F_]+$")


def _scalar(s: str) -> str:
    plain = (_PLAIN.match(s) and not s.endswith((":", " ")) and ": " not in s and not _RESERVED.match(s)
             and not _NUMBER.match(s))
    return s if plain else json.dumps(s, ensure_ascii=False)


def dump(v: Any) -> str:
    return "".join(_lines(v, 0)) 


def _lines(v: Any, indent: int):
    pad = "  " * indent
    if isinstance(v, dict):
        for k in sorted(v):
            e = v[k]
            if isinstance(e, (dict, list)):
                yield f"{pad}{_scalar(k)}:\n"
                yield from _lines(e, indent + (0 if isinstance(e, list) else 1))
            else:
                yield f"{pad}{_scalar(k)}: {_scalar(e)}\n"
    elif isinstance(v, list):
        for e in v:
            if isinstance(e, dict):
                first = True
                for line in _lines(e, indent + 1):
                    yield (f"{pad}- " + line[len(pad) + 2:]) if first else line
                    first = False
            else:
                yield f"{pad}- {_scalar(e)}\n"
