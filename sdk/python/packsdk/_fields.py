"""field(): the tags of a Go struct field, as arguments. A setting of a pack is a dataclass field."""

from __future__ import annotations

import dataclasses
from typing import Any

_MISSING = dataclasses.MISSING


def field(default: Any = _MISSING, *, factory: Any = None, json: str | None = None, env: str | None = None,
          file: str | None = None, template: str | None = None, format: str | None = None, mode: str | None = None,
          parse_as: str | None = None, omitempty: bool = False, skip: bool = False):
    """One setting of the application.

    json       the property's name in the schema (the attribute's name by default); the application's own key
    env        the field is the environment variable NAME
    file       the field — a leaf or a whole subtree — becomes the file at this path in the container
    template   ... rendered from the pack's content/<template> (a *.ptmpl), or, with format="raw", shipped as it is
    format     ... or the subtree written in that format (json, yaml, properties, env, ini, toml, …)
    mode       the file's mode, "0640"
    parse_as   how a template's output is parsed back, where it is
    omitempty  the field is optional; without it the field is required (the Go json tag's omitempty)
    skip       not a setting (json:"-")
    """
    meta = {"json": json, "env": env, "file": file, "template": template, "format": format, "mode": mode,
            "parse_as": parse_as, "omitempty": omitempty, "skip": skip}
    kw = {"metadata": {"pack": meta}}
    if factory is not None:
        return dataclasses.field(default_factory=factory, **kw)
    if default is _MISSING:
        raise TypeError("field() needs a default (the zero value, or a default_factory) so that a pack can be built bare")
    return dataclasses.field(default=default, **kw)
