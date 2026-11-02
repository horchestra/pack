"""field(): the tags of a Go struct field, as arguments. A setting of a pack is a dataclass field."""

from __future__ import annotations

import dataclasses
from typing import Any

_MISSING = dataclasses.MISSING


def field(default: Any = _MISSING, *, factory: Any = None, json: str | None = None, env: str | None = None,
          read_only: bool = False, omitempty: bool = False, skip: bool = False):
    """One setting of the application.

    json       the property's name in the schema (the attribute's name by default); the application's own key
    env        the field is the environment variable NAME
    read_only  the operator does not set it (the Go jsonschema tag's readOnly=true)
    omitempty  the field is optional; without it the field is required (the Go json tag's omitempty)
    skip       not a setting (json:"-")
    """
    meta = {"json": json, "env": env, "read_only": read_only, "omitempty": omitempty, "skip": skip}
    kw = {"metadata": {"pack": meta}}
    if factory is not None:
        return dataclasses.field(default_factory=factory, **kw)
    if default is _MISSING:
        raise TypeError("field() needs a default (the zero value, or a default_factory) so that a pack can be built bare")
    return dataclasses.field(default=default, **kw)
