"""Pack: what an author of a pack writes against. A pack is a dataclass whose attributes are the application's
settings; it may hold another pack in an attribute (Optional makes it optional)."""

from __future__ import annotations

import argparse
import dataclasses
import inspect
import json
import os
import sys
from pathlib import Path
from typing import Any

from . import _docs, _yaml
from ._reflect import SchemaError, hints, is_pack, json_name, reflect, tags, unwrap
from ._vocab import Meta, Root, Rule

DEFAULT_REPO = "packs"  # the prefix of the repositories packs are published under: <repo>/<name>:<version>
ARTIFACT_PACK = "application/vnd.pack.pack.v1+json"
ARTIFACT_SCHEMA = "application/vnd.pack.schema.v1+json"
ARTIFACT_TEMPLATE = "application/vnd.pack.template.v1"
ANNOTATION_IMAGE = "pack.image"
SCHEMA_FILE, PACK_FILE, TEMPLATES_DIR, CONTENT_DIR = "schema.json", "pack.yaml", "templates", "content"


class Pack:
    """Subclass it, decorate the subclass with @dataclass, and say what the pack is:

    set_defaults()  what the zero values cannot say; sets, never adds (it may run twice)
    meta()          the identity: name, version, image
    pack_root()     how it runs, upgrades and rolls back: ports, volumes, readiness, hooks, savepoints …
    pack_compute()  optional: {pointer: CEL formula} that fills a value
    pack_rules()    optional: {pointer: [Rule, …]} that the values must satisfy
    """

    def set_defaults(self) -> None:
        pass

    def meta(self) -> Meta:
        raise NotImplementedError(f"{type(self).__name__} has no meta()")

    def pack_root(self) -> Root:
        raise NotImplementedError(f"{type(self).__name__} has no pack_root()")

    def pack_compute(self) -> dict[str, str]:
        return {}

    def pack_rules(self) -> dict[str, list[Rule]]:
        return {}


# ---- the packs a pack holds

def _holds(p: Pack) -> list[Pack]:
    out = []
    h = hints(type(p))
    for f in dataclasses.fields(type(p)):
        if json_name(f) is None:
            continue
        core = unwrap(h[f.name])
        if is_pack(core):
            out.append(core())
    return out


def packs(root: Pack) -> list[Pack]:
    """The pack and every pack it holds, each once, the pack first. A pack that holds itself is refused."""
    out: list[Pack] = []
    seen: set[str] = set()

    def walk(p: Pack, path: list[str]) -> None:
        name = p.meta().name
        if not name:
            raise SchemaError(f"{type(p).__name__}: meta needs a name")
        if name in path:
            raise SchemaError(f'pack "{name}" holds itself: ' + " → ".join([*path, name]))
        if name in seen:
            return
        seen.add(name)
        out.append(p)
        for sub in _holds(p):
            walk(sub, [*path, name])

    walk(root, [])
    return out


# ---- the artifact

def schema(p: Pack) -> bytes:
    """The pack's schema.json, with its docs/ written into it."""
    s = reflect(p)
    d = _docs.load(_dir(p))
    if d is not None:
        _docs.apply(s, *d)
    text = json.dumps(s, separators=(",", ":"), ensure_ascii=False)
    # encoding/json escapes these in strings; the artifact is the same byte for byte whichever SDK wrote it
    for ch, esc in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        text = text.replace(ch, esc)
    return text.encode()


def annotations(m: Meta) -> dict[str, str]:
    out = {"pack.name": m.name, "pack.version": m.version, "org.opencontainers.image.title": m.name,
           "org.opencontainers.image.version": m.version}
    for k, v in {"description": m.description, "documentation": m.documentation, "source": m.source, "url": m.url,
                 "licenses": m.license, "vendor": m.vendor, "authors": m.authors, "created": m.created}.items():
        if v:
            out["org.opencontainers.image." + k] = v
    for k, v in m.annotations.items():
        if k in out:
            raise SchemaError(f"annotation {k} is written by the sdk from Meta: set the field, not the annotation")
        out[k] = v
    return out


def _dir(p: Pack) -> Path:
    m = p.meta()
    if m.dir:
        return Path(m.dir)
    return Path(inspect.getfile(type(p).meta)).parent


def content(p: Pack) -> dict[str, bytes]:
    """The pack's content/ directory: every regular file under it, by its name relative to it. A symlink is not
    content. The files the fields name must be there."""
    root = _dir(p) / CONTENT_DIR
    out: dict[str, bytes] = {}
    if root.is_dir():
        for path in sorted(root.rglob("*")):
            if path.is_symlink() or not path.is_file():
                continue
            out[path.relative_to(root).as_posix()] = path.read_bytes()
    _check_named(type(p), out)
    return out


def _check_named(tp: type, have: dict[str, bytes]) -> None:
    root = tp().pack_root()
    files = list(root.content)
    for os_packages in root.packages.values():
        files += os_packages.repo + os_packages.content
    missing = [f"{f.path}: {name}" for f in files for name in (f.source, f.template) if name and name not in have]
    if missing:
        raise SchemaError(f"not in {CONTENT_DIR}/: " + ", ".join(sorted(missing)))

def pack_file(p: Pack, repo: str = DEFAULT_REPO) -> dict:
    m = p.meta()
    if not m.name or not m.version:
        raise SchemaError(f"{type(p).__name__}: meta needs a name and a version")
    items: list[dict] = []
    if "://" in m.image:
        raise SchemaError(f'{m.name}: image "{m.image}": a repository of the registry the pack is pushed to, repo:tag, no scheme')
    if m.image:
        items.append({"from": "oci://" + m.image, "annotations": {ANNOTATION_IMAGE: "true"}})
    items.append({"from": "file://" + SCHEMA_FILE, "type": ARTIFACT_SCHEMA})
    if content(p):
        items.append({"from": f"dir://{TEMPLATES_DIR}/", "type": ARTIFACT_TEMPLATE})
    for sub in packs(p)[1:]:
        sm = sub.meta()
        if not sm.version:
            raise SchemaError(f"{sm.name}: meta needs a version")
        items.append({"from": f"oci://{repo}/{sm.name}:{sm.version}"})
    return {"repository": f"{repo}/{m.name}:{m.version}", "type": ARTIFACT_PACK, "annotations": annotations(m), "items": items}


def pack_file_yaml(p: Pack, repo: str = DEFAULT_REPO) -> bytes:
    return _yaml.dump(pack_file(p, repo)).encode()


def generate(directory: str | os.PathLike, *roots: Pack) -> None:
    """Write every pack's directory under directory: <name>/schema.json, <name>/pack.yaml, <name>/templates/*. The
    packs a pack holds are written too, so one call writes the whole tree oci-packer packs."""
    seen: dict[str, Pack] = {}
    for r in roots:
        for p in packs(r):
            seen.setdefault(p.meta().name, p)
    generate_in(directory, DEFAULT_REPO, *(seen[name] for name in sorted(seen)))


def generate_in(directory: str | os.PathLike, repo: str, *pks: Pack) -> None:
    """Write the directories of these packs and of no other, published under repo. A pack is generated and published on
    its own, the packs it holds first."""
    for p in pks:
        name = p.meta().name
        out = Path(directory) / name
        out.mkdir(parents=True, exist_ok=True)
        try:
            (out / SCHEMA_FILE).write_bytes(schema(p))
        except SchemaError as e:
            raise SchemaError(f"{name}: {e}") from None
        (out / PACK_FILE).write_bytes(pack_file_yaml(p, repo))
        for rel, b in content(p).items():
            dst = out / TEMPLATES_DIR / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            dst.write_bytes(b)


# ---- the generator CLI

def main(*roots: Pack, argv: list[str] | None = None) -> int:
    """A ready generator CLI of an author's __main__:

        generate [-o dir] [--repo packs]   write the pack's directory, not of the packs it holds (default .)
        schema <name>       print one pack's schema.json

    The author checks run on the generated directory: `pack check <dir>`."""
    ap = argparse.ArgumentParser(prog=Path(sys.argv[0]).name, description="generate the artifacts of the packs")
    sub = ap.add_subparsers(dest="cmd", required=True)
    g = sub.add_parser("generate", help="write the pack's directory, not of the packs it holds: schema.json, pack.yaml, templates/")
    g.add_argument("-o", "--output", default=".", help="directory to write the artifacts under")
    g.add_argument("--repo", default=DEFAULT_REPO, help="the prefix of the repositories the pack and the packs it holds are published under")
    s = sub.add_parser("schema", help="print one pack's schema.json")
    s.add_argument("name")
    args = ap.parse_args(argv)
    try:
        by_name: dict[str, Pack] = {}
        for r in roots:
            for p in packs(r):
                by_name.setdefault(p.meta().name, p)
        if args.cmd == "generate":
            generate_in(args.output, args.repo, *roots)
            for r in roots:
                print("wrote", Path(args.output) / r.meta().name)
        else:
            if args.name not in by_name:
                raise SchemaError(f'no pack "{args.name}" (have {", ".join(sorted(by_name))})')
            sys.stdout.write(schema(by_name[args.name]).decode())
    except (SchemaError, OSError) as e:
        print(f"{ap.prog}: {e}", file=sys.stderr)
        return 1
    return 0
