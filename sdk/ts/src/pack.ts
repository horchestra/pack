// Pack: the artifact side — schema.json, pack.yaml, templates/ — and the generator CLI. What an author of a pack
// writes against is Pack in reflect.ts: a class whose fields, declared with field(), are the application's
// settings; it may hold another pack in a field (a null default with pack: makes it optional).

import { existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

import { applyDocs, loadDocs } from "./docs.ts";
import { fieldsOf, initFields, Pack, packStack, reflect, SchemaError } from "./reflect.ts";
import { PACK_SCHEME } from "./vocab.ts";
import type { Meta } from "./vocab.ts";
import { dumpYaml } from "./yaml.ts";

export const DEFAULT_REPO = "packs"; // the prefix of the repositories packs are published under: <repo>/<name>:<version>
export const ARTIFACT_PACK = "application/vnd.pack.pack.v1+json";
export const ARTIFACT_SCHEMA = "application/vnd.pack.schema.v1+json";
export const ARTIFACT_TEMPLATE = "application/vnd.pack.template.v1";
export const ANNOTATION_IMAGE = "pack.image";
export const SCHEMA_FILE = "schema.json";
export const PACK_FILE = "pack.yaml";
export const TEMPLATES_DIR = "templates";
export const CONTENT_DIR = "content";

// ---- the packs a pack holds

function holds(p: Pack): Pack[] {
  const out: Pack[] = [];
  for (const f of fieldsOf(p)) {
    if (!f.json) continue;
    if (f.pack) out.push(new f.pack());
  }
  return out;
}

/** The pack and every pack it holds, each once, the pack first. A pack that holds itself is refused. */
export function packs(root: Pack): Pack[] {
  const out: Pack[] = [];
  const seen = new Set<string>();

  function walk(p: Pack, path: string[]): void {
    initFields(p);
    const name = p.meta().name;
    if (!name) throw new SchemaError(`${p.constructor.name}: meta needs a name`);
    if (path.includes(name)) {
      throw new SchemaError(`pack "${name}" holds itself: ${[...path, name].join(" → ")}`);
    }
    if (seen.has(name)) return;
    seen.add(name);
    out.push(p);
    for (const sub of holds(p)) walk(sub, [...path, name]);
  }

  walk(root, []);
  return out;
}

// ---- the artifact

/** The pack's schema.json, with its docs/ written into it. */
export function schema(p: Pack): Buffer {
  const s = reflect(p);
  const d = loadDocs(packDir(p));
  if (d !== null) applyDocs(s, ...d);
  let text = JSON.stringify(s);
  // encoding/json escapes these in strings; the artifact is the same byte for byte whichever SDK wrote it
  for (const [ch, esc] of [["<", "\\u003c"], [">", "\\u003e"], ["&", "\\u0026"], ["\\u2028", "\\u2028"], ["\\u2029", "\\u2029"]] as const) {
    text = text.split(ch).join(esc);
  }
  return Buffer.from(text, "utf8");
}

export function annotations(m: Meta): Record<string, string> {
  const out: Record<string, string> = {
    "pack.name": m.name, "pack.version": m.version,
    "org.opencontainers.image.title": m.name, "org.opencontainers.image.version": m.version,
  };
  const pairs: [string, string | undefined][] = [
    ["description", m.description], ["documentation", m.documentation], ["source", m.source], ["url", m.url],
    ["licenses", m.license], ["vendor", m.vendor], ["authors", m.authors], ["created", m.created],
  ];
  for (const [k, v] of pairs) if (v) out["org.opencontainers.image." + k] = v;
  for (const [k, v] of Object.entries(m.annotations ?? {})) {
    if (k in out) {
      throw new SchemaError(`annotation ${k} is written by the sdk from Meta: set the field, not the annotation`);
    }
    out[k] = v;
  }
  return out;
}

const DIR = Symbol("dir");

// The directory of the file the pack's class is defined in: the first frame of its construction's stack that is not
// the sdk's own.
function dirFromStack(p: Pack): string {
  const srcDir = dirname(fileURLToPath(import.meta.url));
  for (const line of packStack(p).split("\n")) {
    const m = line.match(/\((file:\/\/[^)]+?):\d+:\d+\)$/) ?? line.match(/at (file:\/\/\S+?):\d+:\d+$/);
    if (!m) continue;
    const file = fileURLToPath(m[1]);
    if (file.startsWith(srcDir + sep)) continue; // the sdk's own frame
    return dirname(file);
  }
  return process.cwd();
}

/** The pack's directory: Meta.dir when set, else where the pack's class is defined. */
export function packDir(p: Pack): string {
  const m = p.meta();
  if (m.dir) return m.dir;
  const self = p as unknown as Record<symbol, string | undefined>;
  let d = self[DIR];
  if (!d) {
    d = dirFromStack(p);
    self[DIR] = d;
  }
  return d;
}

/** The pack's content/ directory: every regular file under it, by its name relative to it. A symlink is not
 * content. The files the fields name must be there. */
export function content(p: Pack): Map<string, Buffer> {
  const root = join(packDir(p), CONTENT_DIR);
  const out = new Map<string, Buffer>();
  if (existsSync(root)) {
    const walk = (dir: string): string[] =>
      readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
        const path = join(dir, e.name);
        if (e.isDirectory()) return walk(path);
        if (lstatSync(path).isSymbolicLink() || !e.isFile()) return [];
        return [path];
      });
    for (const path of walk(root).sort()) {
      out.set(relative(root, path).split(sep).join("/"), readFileSync(path));
    }
  }
  checkNamed(p.constructor as new () => Pack, out);
  return out;
}

function checkNamed(tp: new () => Pack, have: Map<string, Buffer>): void {
  const root = new tp().packRoot();
  const files = [...(root.content ?? [])];
  for (const osPackages of Object.values(root.packages ?? {})) {
    files.push(...(osPackages.repo ?? []), ...(osPackages.content ?? []));
  }
  const missing: string[] = [];
  for (const f of files) {
    for (const name of [f.source, f.template]) {
      if (name && !have.has(name)) missing.push(`${f.path}: ${name}`);
    }
  }
  if (missing.length) {
    throw new SchemaError(`not in ${CONTENT_DIR}/: ${missing.sort().join(", ")}`);
  }
}

export function packFile(p: Pack, repo = DEFAULT_REPO): Record<string, unknown> {
  const m = p.meta();
  if (!m.name || !m.version) throw new SchemaError(`${p.constructor.name}: meta needs a name and a version`);
  const items: Record<string, unknown>[] = [];
  if (m.image?.includes("://")) {
    throw new SchemaError(
      `${m.name}: image "${m.image}": a repository of the registry the pack is pushed to, repo:tag, no scheme`);
  }
  if (m.image) items.push({ from: "oci://" + m.image, annotations: { [ANNOTATION_IMAGE]: "true" } });
  items.push({ from: "file://" + SCHEMA_FILE, type: ARTIFACT_SCHEMA });
  if (content(p).size) items.push({ from: `dir://${TEMPLATES_DIR}/`, type: ARTIFACT_TEMPLATE });
  for (const sub of packs(p).slice(1)) {
    const sm = sub.meta();
    if (!sm.version) throw new SchemaError(`${sm.name}: meta needs a version`);
    items.push({ from: `oci://${repo}/${sm.name}:${sm.version}` });
  }
  return {
    repository: `${repo}/${m.name}:${m.version}`, type: ARTIFACT_PACK, annotations: annotations(m), items,
  };
}

export function packFileYaml(p: Pack, repo = DEFAULT_REPO): Buffer {
  return Buffer.from(dumpYaml(packFile(p, repo)), "utf8");
}

/** Write every pack's directory under directory: <name>/schema.json, <name>/pack.yaml, <name>/templates/*. The
 * packs a pack holds are written too, so one call writes the whole tree oci-packer packs. */
export function generate(directory: string, ...roots: Pack[]): void {
  const seen = new Map<string, Pack>();
  for (const r of roots) {
    for (const p of packs(r)) {
      if (!seen.has(p.meta().name)) seen.set(p.meta().name, p);
    }
  }
  generateIn(directory, DEFAULT_REPO, ...[...seen.keys()].sort().map((name) => seen.get(name)!));
}

/** Write the directories of these packs and of no other, published under repo. A pack is generated and published on
 * its own, the packs it holds first. */
export function generateIn(directory: string, repo: string, ...pks: Pack[]): void {
  for (const p of pks) {
    const name = p.meta().name;
    const out = join(directory, name);
    mkdirSync(out, { recursive: true });
    try {
      writeFileSync(join(out, SCHEMA_FILE), schema(p));
    } catch (e) {
      if (e instanceof SchemaError) throw new SchemaError(`${name}: ${e.message}`);
      throw e;
    }
    writeFileSync(join(out, PACK_FILE), packFileYaml(p, repo));
    for (const [rel, b] of content(p)) {
      const dst = join(out, TEMPLATES_DIR, ...rel.split("/"));
      mkdirSync(dirname(dst), { recursive: true });
      writeFileSync(dst, b);
    }
  }
}

// ---- the generator CLI

/** A ready generator CLI of an author's entry point:
 *
 *     generate [-o dir] [--repo packs]   write the pack's directory, not of the packs it holds (default .)
 *     schema <name>                      print one pack's schema.json
 *
 * The author checks run on the generated directory: `pack check <dir>`. */
export async function main(...roots: Pack[]): Promise<number> {
  const prog = "pack-sdk";
  const { values, positionals } = parseArgs({
    allowPositionals: true,
    options: { output: { type: "string", short: "o", default: "." }, repo: { type: "string", default: DEFAULT_REPO } },
  });
  const [cmd, name] = positionals;
  try {
    const byName = new Map<string, Pack>();
    for (const r of roots) {
      for (const p of packs(r)) if (!byName.has(p.meta().name)) byName.set(p.meta().name, p);
    }
    if (cmd === "generate") {
      generateIn(values.output!, values.repo!, ...roots);
      for (const r of roots) console.log("wrote", join(values.output!, r.meta().name));
    } else if (cmd === "schema") {
      if (!name || !byName.has(name)) {
        throw new SchemaError(`no pack "${name}" (have ${[...byName.keys()].sort().join(", ")})`);
      }
      process.stdout.write(schema(byName.get(name)!));
    } else {
      console.error(`${prog}: generate [-o dir] [--repo packs] | schema <name>`);
      return 1;
    }
  } catch (e) {
    if (e instanceof SchemaError || (e as NodeJS.ErrnoException).code) {
      console.error(`${prog}: ${(e as Error).message}`);
      return 1;
    }
    throw e;
  }
  return 0;
}

// PACK_SCHEME is part of the author's vocabulary of references.
export { PACK_SCHEME };
