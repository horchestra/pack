"""docs: the documentation of a pack's settings, in languages — docs/<lang>.yaml next to the pack's source, keyed by
the JSON Pointer of a field — written into the schema the way the Go sdk writes it (sdk/docs): the base language as
title, description and examples, the others as x-pack-i18n. The YAML is the subset the Go sdk reads, read here
without a library, and read the same."""

from __future__ import annotations

import re
from pathlib import Path

from ._reflect import SchemaError

DIR = "docs"
DEFAULT_BASE = "en"
KEY_I18N, KEY_GROUP, KEY_GROUPS, KEY_LANGS = "x-pack-i18n", "x-pack-group", "x-pack-groups", "x-pack-langs"

_LANG = re.compile(r"^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$")
_PLAIN_KEY = re.compile(r"^[^\s\-?:,\[\]{}#&*!|>'\"%@`]")
_INTEGER = re.compile(r"^-?(0|[1-9][0-9]*)$")
_FRACTION = re.compile(r"^-?(0|[1-9][0-9]*)\.[0-9]*[1-9]$")
# what a number of examples may be, in every SDK alike (sdk/docs.NumberRule): the text each writes back unchanged
NUMBER_RULE = ("an integer of int64, or -?digits.digits: no exponent, no trailing zero, at most 15 significant digits, "
               "at least 0.0001")


def _canonical_fraction(x: str) -> bool:
    whole, _, frac = x.removeprefix("-").partition(".")
    if whole == "0" and len(frac) - len(frac.lstrip("0")) > 3:
        return False
    return len((whole + frac).lstrip("0")) <= 15


# ---- the YAML of docs/

class _Parser:
    def __init__(self, src: str):
        self.lines = src.replace("\r\n", "\n").split("\n")
        self.i = 0

    def err(self, msg: str) -> SchemaError:
        return SchemaError(f"line {self.i + 1}: {msg}")

    def next(self) -> bool:
        while self.i < len(self.lines):
            t = self.lines[self.i].lstrip(" ")
            if t and t[0] != "#":
                return True
            self.i += 1
        return False

    def indent(self) -> int:
        line = self.lines[self.i]
        n = len(line) - len(line.lstrip(" "))
        if n < len(line) and line[n] == "\t":
            raise self.err("a tab in the indentation")
        return n

    def mapping(self, indent: int) -> dict:
        out: dict = {}
        while self.next():
            n = self.indent()
            if n < indent:
                break
            if n > indent:
                raise self.err("unexpected indentation")
            line = self.lines[self.i][n:]
            if line == "-" or line.startswith("- "):
                raise self.err("a list item where a key was expected")
            key, rest = self.key(line)
            if key in out:
                raise self.err(f'"{key}" twice')
            if rest == "":
                self.i += 1
                if not self.next():
                    raise SchemaError(f'line {self.i}: "{key}" has no value')
                child = self.indent()
                if child <= indent:
                    raise self.err(f'"{key}" has no value')
                first = self.lines[self.i][child:]
                v = self.list(child) if first == "-" or first.startswith("- ") else self.mapping(child)
            elif rest in ("|", "|-"):
                self.i += 1
                v = self.block(indent)
            else:
                v = self.scalar(rest)
                self.i += 1
            out[key] = v
        return out

    def list(self, indent: int) -> list[str]:
        out: list[str] = []
        while self.next():
            n = self.indent()
            if n < indent:
                break
            if n > indent:
                raise self.err("unexpected indentation")
            line = self.lines[self.i][n:]
            if not line.startswith("- "):
                raise self.err("a list item without a value" if line == "-" else "a key where a list item was expected")
            rest = line[2:].lstrip(" ")
            if rest in ("|", "|-"):
                raise self.err("a block in a list: write the item on its line")
            out.append(self.scalar(rest))
            self.i += 1
        return out

    def block(self, indent: int) -> str:
        out: list[str] = []
        depth = -1
        while self.i < len(self.lines):
            line = self.lines[self.i]
            if not line.lstrip(" "):
                out.append("")
                self.i += 1
                continue
            n = len(line) - len(line.lstrip(" "))
            if depth < 0:
                if n <= indent:
                    break
                depth = n
            if n < depth:
                break
            out.append(line[depth:])
            self.i += 1
        while out and not out[-1]:
            out.pop()
        return "\n".join(out)

    def key(self, line: str) -> tuple[str, str]:
        if line[0] == '"':
            k, after = self.double_quoted(line)
            if not after.startswith(":"):
                raise self.err("a quoted key without ':'")
            return k, _value(after[1:])
        if not _PLAIN_KEY.match(line) and not line.startswith("/"):
            raise self.err("a key that is not plain: quote it")
        i = line.find(": ")
        if i >= 0:
            return line[:i].rstrip(" "), _value(line[i + 1:])
        if line.endswith(":"):
            return line[:-1].rstrip(" "), ""
        raise self.err("no ':' after the key")

    def scalar(self, s: str) -> str:
        if s[0] == '"':
            v, after = self.double_quoted(s)
            self.tail(after)
            return v
        if s[0] == "'":
            v, after = self.single_quoted(s)
            self.tail(after)
            return v
        if s[0] in "[{&*!>|%@`":
            raise self.err(f'"{s}": not in the YAML of docs/ (block style and plain or quoted scalars only)')
        i = s.find(" #")
        if i >= 0:
            s = s[:i]
        s = s.rstrip(" ")
        if ": " in s or s.endswith(":"):
            raise self.err(f'"{s}": a \':\' in a plain scalar — quote it')
        return s

    def tail(self, after: str) -> None:
        after = after.lstrip(" ")
        if after and after[0] != "#":
            raise self.err(f'"{after}" after a quoted scalar')

    def double_quoted(self, s: str) -> tuple[str, str]:
        out: list[str] = []
        i = 1
        while i < len(s):
            c = s[i]
            if c == '"':
                return "".join(out), s[i + 1:]
            if c == "\\":
                if i + 1 >= len(s):
                    raise self.err("an escape at the end of the line")
                i += 1
                e = s[i]
                if e in '"\\/':
                    out.append(e)
                elif e == "n":
                    out.append("\n")
                elif e == "t":
                    out.append("\t")
                elif e == "r":
                    out.append("\r")
                elif e == "u":
                    h = s[i + 1:i + 5]
                    if len(h) < 4 or not re.fullmatch(r"[0-9A-Fa-f]{4}", h):
                        raise self.err(f"\\u{h}: not hex")
                    out.append(chr(int(h, 16)))
                    i += 4
                else:
                    raise self.err(f"\\{e}: not an escape of docs/")
            else:
                out.append(c)
            i += 1
        raise self.err("a double-quoted scalar ends on its line")

    def single_quoted(self, s: str) -> tuple[str, str]:
        out: list[str] = []
        i = 1
        while i < len(s):
            if s[i] == "'":
                if i + 1 < len(s) and s[i + 1] == "'":
                    out.append("'")
                    i += 2
                    continue
                return "".join(out), s[i + 1:]
            out.append(s[i])
            i += 1
        raise self.err("a single-quoted scalar ends on its line")


def _value(s: str) -> str:
    s = s.strip(" ")
    return "" if s.startswith("#") else s


def parse(src: str) -> dict:
    """A document of the subset: a mapping at its top; every scalar a string."""
    p = _Parser(src)
    out = p.mapping(0)
    if p.next():
        raise p.err("unexpected indentation")
    return out


# ---- the files

class File:
    def __init__(self, lang: str):
        self.lang = lang
        self.base = False
        self.fields: dict[str, dict] = {}  # pointer → {title, description, examples, group}, in the order of the file
        self.groups: dict[str, str] = {}
        self.rules: dict[str, str] = {}
        self.diagnose: dict[str, str] = {}


def parse_file(lang: str, src: str) -> File:
    f = File(lang)
    for k, v in parse(src).items():
        if k == "base":
            if v != "true":
                raise SchemaError("base: true or nothing")
            f.base = True
        elif k in ("groups", "rules", "diagnose"):
            if not isinstance(v, dict) or not all(isinstance(x, str) for x in v.values()):
                raise SchemaError(f"{k}: a mapping of an id to its text")
            setattr(f, k, v)
        elif k.startswith("/"):
            f.fields[k] = _entry(k, v)
        else:
            raise SchemaError(f'"{k}": a key is a JSON Pointer of a field (/…, / the pack), base, groups, rules or diagnose')
    return f


def _entry(ptr: str, v) -> dict:
    if not isinstance(v, dict):
        raise SchemaError(f"{ptr}: a mapping of title, description, examples, group")
    e = {"title": "", "description": "", "examples": [], "group": ""}
    for k, x in v.items():
        if k in ("title", "description", "group"):
            if not isinstance(x, str):
                raise SchemaError(f"{ptr}: {k}: a text")
            if k == "title" and "\n" in x:
                raise SchemaError(f"{ptr}: title: one line")
            e[k] = x
        elif k == "examples":
            if not isinstance(x, list):
                raise SchemaError(f"{ptr}: examples: a list")
            e[k] = x
        else:
            raise SchemaError(f'{ptr}: "{k}": a field has title, description, examples and group')
    return e


def load(directory: Path) -> tuple[File, list[File]] | None:
    """The base language and the others (sorted) of directory/docs/; None when the pack has no docs/."""
    root = directory / DIR
    if not root.is_dir():
        return None
    files: list[File] = []
    for path in sorted(root.iterdir()):
        if path.is_dir() or path.name.startswith("."):
            continue
        lang = path.name.removesuffix(".yaml")
        if not path.name.endswith(".yaml") or not _LANG.match(lang):
            raise SchemaError(f"{DIR}/{path.name}: a file of {DIR}/ is <lang>.yaml (en.yaml, ru.yaml, pt-BR.yaml)")
        try:
            files.append(parse_file(lang, path.read_text(encoding="utf-8")))
        except SchemaError as e:
            raise SchemaError(f"{DIR}/{path.name}: {e}") from None
    if not files:
        return None
    bases = [f for f in files if f.base]
    if len(bases) > 1:
        raise SchemaError(f"{DIR}/: {bases[0].lang}.yaml and {bases[1].lang}.yaml both say base: true")
    if not bases:
        bases = [f for f in files if f.lang == DEFAULT_BASE]
    if not bases:
        raise SchemaError(f"{DIR}/: no base language: write {DEFAULT_BASE}.yaml, or say base: true in one file")
    base = bases[0]
    base.base = True
    others = []
    for f in files:
        if f is base:
            continue
        for ptr, e in f.fields.items():
            if e["examples"] or e["group"]:
                raise SchemaError(f"{DIR}/{f.lang}.yaml: {ptr}: examples and group are the base language's ({base.lang}.yaml)")
        for gid in f.groups:
            if gid not in base.groups:
                raise SchemaError(f'{DIR}/{f.lang}.yaml: groups: "{gid}" is not a group of {base.lang}.yaml')
        others.append(f)
    others.sort(key=lambda f: f.lang)
    return base, others


# ---- the schema

def lookup(root: dict, ptr: str) -> dict:
    if ptr == "/":
        return root
    node = root
    for seg in ptr.removeprefix("/").split("/"):
        seg = seg.replace("~1", "/").replace("~0", "~")
        if "$ref" in node:
            raise SchemaError(f"a field of a held pack: it is documented in that pack's {DIR}/")
        props = node.get("properties")
        if props is None:
            raise SchemaError(f'"{seg}": not an object')
        if seg not in props:
            raise SchemaError(f'"{seg}": no such field')
        node = props[seg]
    return node


def walk(root: dict):
    """The pack and its fields, depth first in the order of the schema; not into a held pack."""
    def rec(ptr: str, node: dict):
        yield ptr, node
        if "$ref" in node:
            return
        prefix = ptr.rstrip("/")
        for k, child in node.get("properties", {}).items():
            yield from rec(prefix + "/" + k.replace("~", "~0").replace("/", "~1"), child)
    yield from rec("/", root)


def _examples(node: dict, xs: list[str]) -> list:
    if "$ref" in node or node.get("format") == "password" or node.get("writeOnly"):
        raise SchemaError("a held pack and a secret have no examples")
    out: list = []
    t = node.get("type")
    for x in xs:
        if t == "string":
            out.append(x)
        elif t in ("integer", "number"):
            if _INTEGER.match(x):
                n = int(x)
                if not -(1 << 63) <= n < (1 << 63):
                    raise SchemaError(f'"{x}": {NUMBER_RULE}')
                out.append(n)
            elif t == "number" and _FRACTION.match(x) and _canonical_fraction(x):
                out.append(float(x))  # its repr is x: at most 15 digits, between 0.0001 and 1e15
            else:
                raise SchemaError(f'"{x}": {NUMBER_RULE}')
        elif t == "boolean":
            if x not in ("true", "false"):
                raise SchemaError(f'"{x}" is not true or false')
            out.append(x == "true")
        else:
            raise SchemaError("only a string, a number or a boolean has examples here")
    return out


def apply(root: dict, base: File, others: list[File]) -> None:
    """Write the documentation into a pack's schema, as the Go sdk's docs.Apply does."""
    for f in [base, *others]:
        for ptr, e in f.fields.items():
            try:
                node = lookup(root, ptr)
                if f.base:
                    if e["title"]:
                        node["title"] = e["title"]
                    if e["description"]:
                        node["description"] = e["description"]
                    if e["examples"]:
                        node["examples"] = _examples(node, e["examples"])
                    if e["group"]:
                        node[KEY_GROUP] = e["group"]
                else:
                    t = {k: e[k] for k in ("description", "title") if e[k]}
                    if t:
                        node.setdefault(KEY_I18N, {})[f.lang] = t
            except SchemaError as err:
                raise SchemaError(f"{DIR}/{f.lang}.yaml: {ptr}: {err}") from None
    used: list[str] = []
    for _, node in walk(root):
        g = node.get(KEY_GROUP)
        if g and g not in used:
            used.append(g)
    groups = []
    for gid in used:
        title = base.groups.get(gid, "")
        if not title:
            raise SchemaError(f'{DIR}/{base.lang}.yaml: group "{gid}" has no title under groups:')
        g = {"id": gid, "title": title}
        tr = {f.lang: {"title": f.groups[gid]} for f in others if f.groups.get(gid)}
        if tr:
            g[KEY_I18N] = tr
        groups.append(g)
    for gid in base.groups:
        if gid not in used:
            raise SchemaError(f'{DIR}/{base.lang}.yaml: group "{gid}": no field is in it')
    if groups:
        root[KEY_GROUPS] = groups
    by_id = {}
    for _, node in walk(root):
        for r in node.get("x-pack-rules", []):
            if r.get("id"):
                by_id[r["id"]] = r
    for f in [base, *others]:
        for rid, msg in f.rules.items():
            r = by_id.get(rid)
            if r is None:
                raise SchemaError(f'{DIR}/{f.lang}.yaml: rules: "{rid}" is not the id of a rule of the pack')
            if not msg:
                continue
            if f.base:
                r["message"] = msg
            else:
                r.setdefault(KEY_I18N, {})[f.lang] = {"message": msg}
    diags = {d["id"]: d for d in root.get("x-pack-diagnose", [])}
    for f in [base, *others]:
        for did, cause in f.diagnose.items():
            d = diags.get(did)
            if d is None:
                raise SchemaError(f'{DIR}/{f.lang}.yaml: diagnose: "{did}" is not the id of a diagnosis of the pack')
            if not cause:
                continue
            if f.base:
                d["cause"] = cause
            else:
                d.setdefault(KEY_I18N, {})[f.lang] = {"cause": cause}
    root[KEY_LANGS] = [base.lang, *(f.lang for f in others)]
