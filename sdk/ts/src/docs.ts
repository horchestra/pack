// docs: the documentation of a pack's settings, in languages — docs/<lang>.yaml next to the pack's source, keyed by
// the JSON Pointer of a field — written into the schema the way the Go sdk writes it (sdk/docs): the base language as
// title, description and examples, the others as x-pack-i18n. The YAML is the subset the Go sdk reads, read here
// without a library, and read the same.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { SchemaError } from "./reflect.ts";
import { ACTION_ORDER, PARAM_ORDER, ordered } from "./vocab.ts";

export const DOCS_DIR = "docs";
export const DEFAULT_BASE = "en";
export const KEY_I18N = "x-pack-i18n";
export const KEY_GROUP = "x-pack-group";
export const KEY_GROUPS = "x-pack-groups";
export const KEY_LANGS = "x-pack-langs";

const LANG = /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$/;
const PLAIN_KEY = /^[^\s\-?:,[\]{}#&*!|>'"%@`]/;
const INTEGER = /^-?(0|[1-9][0-9]*)$/;
const FRACTION = /^-?(0|[1-9][0-9]*)\.[0-9]*[1-9]$/;
// what a number of examples may be, in every SDK alike (sdk/docs.NumberRule): the text each writes back unchanged
export const NUMBER_RULE =
  "an integer of int64, or -?digits.digits: no exponent, no trailing zero, at most 15 significant digits, at least 0.0001";

function canonicalFraction(x: string): boolean {
  const s = x.startsWith("-") ? x.slice(1) : x;
  const [whole, , frac] = s.split(".");
  const [w, f] = [whole, frac ?? ""];
  if (w === "0" && f.length - f.replace(/^0+/, "").length > 3) return false;
  return (w + f).replace(/^0+/, "").length <= 15;
}

// ---- the YAML of docs/

type Dict = Record<string, unknown>;

class Parser {
  lines: string[];
  i = 0;

  constructor(src: string) {
    this.lines = src.replace(/\r\n/g, "\n").split("\n");
  }

  err(msg: string): SchemaError {
    return new SchemaError(`line ${this.i + 1}: ${msg}`);
  }

  next(): boolean {
    while (this.i < this.lines.length) {
      const t = this.lines[this.i].replace(/^ +/, "");
      if (t && t[0] !== "#") return true;
      this.i++;
    }
    return false;
  }

  indent(): number {
    const line = this.lines[this.i];
    const n = line.length - line.replace(/^ +/, "").length;
    if (n < line.length && line[n] === "\t") throw this.err("a tab in the indentation");
    return n;
  }

  mapping(indent: number): Dict {
    const out: Dict = {};
    while (this.next()) {
      const n = this.indent();
      if (n < indent) break;
      if (n > indent) throw this.err("unexpected indentation");
      const line = this.lines[this.i].slice(n);
      if (line === "-" || line.startsWith("- ")) throw this.err("a list item where a key was expected");
      const [key, rest] = this.key(line);
      if (key in out) throw this.err(`"${key}" twice`);
      let v: unknown;
      if (rest === "") {
        this.i++;
        if (!this.next()) throw new SchemaError(`line ${this.i}: "${key}" has no value`);
        const child = this.indent();
        if (child <= indent) throw this.err(`"${key}" has no value`);
        const first = this.lines[this.i].slice(child);
        v = first === "-" || first.startsWith("- ") ? this.list(child) : this.mapping(child);
      } else if (rest === "|" || rest === "|-") {
        this.i++;
        v = this.block(indent);
      } else {
        v = this.scalar(rest);
        this.i++;
      }
      out[key] = v;
    }
    return out;
  }

  list(indent: number): string[] {
    const out: string[] = [];
    while (this.next()) {
      const n = this.indent();
      if (n < indent) break;
      if (n > indent) throw this.err("unexpected indentation");
      const line = this.lines[this.i].slice(n);
      if (!line.startsWith("- ")) {
        throw this.err(line === "-" ? "a list item without a value" : "a key where a list item was expected");
      }
      const rest = line.slice(2).replace(/^ +/, "");
      if (rest === "|" || rest === "|-") throw this.err("a block in a list: write the item on its line");
      out.push(this.scalar(rest));
      this.i++;
    }
    return out;
  }

  block(indent: number): string {
    const out: string[] = [];
    let depth = -1;
    while (this.i < this.lines.length) {
      const line = this.lines[this.i];
      if (!line.replace(/^ +/, "")) {
        out.push("");
        this.i++;
        continue;
      }
      const n = line.length - line.replace(/^ +/, "").length;
      if (depth < 0) {
        if (n <= indent) break;
        depth = n;
      }
      if (n < depth) break;
      out.push(line.slice(depth));
      this.i++;
    }
    while (out.length && !out[out.length - 1]) out.pop();
    return out.join("\n");
  }

  key(line: string): [string, string] {
    if (line[0] === '"') {
      const [k, after] = this.doubleQuoted(line);
      if (!after.startsWith(":")) throw this.err("a quoted key without ':'");
      return [k, value(after.slice(1))];
    }
    if (!PLAIN_KEY.test(line) && !line.startsWith("/")) throw this.err("a key that is not plain: quote it");
    const i = line.indexOf(": ");
    if (i >= 0) return [line.slice(0, i).replace(/ +$/, ""), value(line.slice(i + 1))];
    if (line.endsWith(":")) return [line.slice(0, -1).replace(/ +$/, ""), ""];
    throw this.err("no ':' after the key");
  }

  scalar(s: string): string {
    if (s[0] === '"') {
      const [v, after] = this.doubleQuoted(s);
      this.tail(after);
      return v;
    }
    if (s[0] === "'") {
      const [v, after] = this.singleQuoted(s);
      this.tail(after);
      return v;
    }
    if ("[{&*!>|%@`".includes(s[0])) {
      throw this.err(`"${s}": not in the YAML of docs/ (block style and plain or quoted scalars only)`);
    }
    const i = s.indexOf(" #");
    if (i >= 0) s = s.slice(0, i);
    s = s.replace(/ +$/, "");
    if (s.includes(": ") || s.endsWith(":")) throw this.err(`"${s}": a ':' in a plain scalar — quote it`);
    return s;
  }

  tail(after: string): void {
    after = after.replace(/^ +/, "");
    if (after && after[0] !== "#") throw this.err(`"${after}" after a quoted scalar`);
  }

  doubleQuoted(s: string): [string, string] {
    const out: string[] = [];
    let i = 1;
    while (i < s.length) {
      const c = s[i];
      if (c === '"') return [out.join(""), s.slice(i + 1)];
      if (c === "\\") {
        if (i + 1 >= s.length) throw this.err("an escape at the end of the line");
        i++;
        const e = s[i];
        if ('"\\/'.includes(e)) {
          out.push(e);
        } else if (e === "n") {
          out.push("\n");
        } else if (e === "t") {
          out.push("\t");
        } else if (e === "r") {
          out.push("\r");
        } else if (e === "u") {
          const h = s.slice(i + 1, i + 5);
          if (h.length < 4 || !/^[0-9A-Fa-f]{4}$/.test(h)) throw this.err(`\\u${h}: not hex`);
          out.push(String.fromCharCode(parseInt(h, 16)));
          i += 4;
        } else {
          throw this.err(`\\${e}: not an escape of docs/`);
        }
      } else {
        out.push(c);
      }
      i++;
    }
    throw this.err("a double-quoted scalar ends on its line");
  }

  singleQuoted(s: string): [string, string] {
    const out: string[] = [];
    let i = 1;
    while (i < s.length) {
      if (s[i] === "'") {
        if (i + 1 < s.length && s[i + 1] === "'") {
          out.push("'");
          i += 2;
          continue;
        }
        return [out.join(""), s.slice(i + 1)];
      }
      out.push(s[i]);
      i++;
    }
    throw this.err("a single-quoted scalar ends on its line");
  }
}

function value(s: string): string {
  s = s.replace(/^ +| +$/g, "");
  return s.startsWith("#") ? "" : s;
}

/** A document of the subset: a mapping at its top; every scalar a string. */
export function parseDocsYaml(src: string): Dict {
  const p = new Parser(src);
  const out = p.mapping(0);
  if (p.next()) throw p.err("unexpected indentation");
  return out;
}

// ---- the files

export interface DocEntry {
  title: string;
  description: string;
  examples: string[];
  group: string;
}

export class DocFile {
  base = false;
  fields: [string, DocEntry][] = []; // [pointer, entry], in the order of the file
  groups: Record<string, string> = {};
  rules: Record<string, string> = {};
  diagnose: Record<string, string> = {};
  actions: Record<string, string> = {}; // an action's description by its name, a parameter's by <action>/<param>
  lang: string;
  constructor(lang: string) {
    this.lang = lang;
  }
}

export function parseDocFile(lang: string, src: string): DocFile {
  const f = new DocFile(lang);
  for (const [k, v] of Object.entries(parseDocsYaml(src))) {
    if (k === "base") {
      if (v !== "true") throw new SchemaError("base: true or nothing");
      f.base = true;
    } else if (k === "groups" || k === "rules" || k === "diagnose" || k === "actions") {
      if (typeof v !== "object" || v === null || Array.isArray(v)
          || !Object.values(v as Dict).every((x) => typeof x === "string")) {
        throw new SchemaError(`${k}: a mapping of an id to its text`);
      }
      f[k] = v as Record<string, string>;
    } else if (k.startsWith("/")) {
      f.fields.push([k, entry(k, v)]);
    } else {
      throw new SchemaError(
        `"${k}": a key is a JSON Pointer of a field (/…, / the pack), base, groups, rules, diagnose or actions`);
    }
  }
  return f;
}

function entry(ptr: string, v: unknown): DocEntry {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    throw new SchemaError(`${ptr}: a mapping of title, description, examples, group`);
  }
  const e: DocEntry = { title: "", description: "", examples: [], group: "" };
  for (const [k, x] of Object.entries(v as Dict)) {
    if (k === "title" || k === "description" || k === "group") {
      if (typeof x !== "string") throw new SchemaError(`${ptr}: ${k}: a text`);
      if (k === "title" && x.includes("\n")) throw new SchemaError(`${ptr}: title: one line`);
      e[k] = x;
    } else if (k === "examples") {
      if (!Array.isArray(x)) throw new SchemaError(`${ptr}: examples: a list`);
      e.examples = x;
    } else {
      throw new SchemaError(`${ptr}: "${k}": a field has title, description, examples and group`);
    }
  }
  return e;
}

/** The base language and the others (sorted) of directory/docs/; null when the pack has no docs/. */
export function loadDocs(directory: string): [DocFile, DocFile[]] | null {
  const root = join(directory, DOCS_DIR);
  let names: string[];
  try {
    names = readdirSync(root).sort();
  } catch {
    return null;
  }
  const files: DocFile[] = [];
  for (const name of names) {
    if (name.startsWith(".")) continue;
    const lang = name.replace(/\.yaml$/, "");
    if (!name.endsWith(".yaml") || !LANG.test(lang)) {
      throw new SchemaError(`${DOCS_DIR}/${name}: a file of ${DOCS_DIR}/ is <lang>.yaml (en.yaml, ru.yaml, pt-BR.yaml)`);
    }
    let src: string;
    try {
      src = readText(join(root, name));
    } catch {
      continue; // a directory or an unreadable file is not a docs file
    }
    try {
      files.push(parseDocFile(lang, src));
    } catch (e) {
      if (e instanceof SchemaError) throw new SchemaError(`${DOCS_DIR}/${name}: ${e.message}`);
      throw e;
    }
  }
  if (!files.length) return null;
  let bases = files.filter((f) => f.base);
  if (bases.length > 1) {
    throw new SchemaError(`${DOCS_DIR}/: ${bases[0].lang}.yaml and ${bases[1].lang}.yaml both say base: true`);
  }
  if (!bases.length) bases = files.filter((f) => f.lang === DEFAULT_BASE);
  if (!bases.length) {
    throw new SchemaError(`${DOCS_DIR}/: no base language: write ${DEFAULT_BASE}.yaml, or say base: true in one file`);
  }
  const base = bases[0];
  base.base = true;
  const others: DocFile[] = [];
  for (const f of files) {
    if (f === base) continue;
    for (const [ptr, e] of f.fields) {
      if (e.examples.length || e.group) {
        throw new SchemaError(
          `${DOCS_DIR}/${f.lang}.yaml: ${ptr}: examples and group are the base language's (${base.lang}.yaml)`);
      }
    }
    for (const gid of Object.keys(f.groups)) {
      if (!(gid in base.groups)) {
        throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: groups: "${gid}" is not a group of ${base.lang}.yaml`);
      }
    }
    others.push(f);
  }
  others.sort((a, b) => (a.lang < b.lang ? -1 : a.lang > b.lang ? 1 : 0));
  return [base, others];
}

function readText(path: string): string {
  return readFileSync(path, "utf8");
}

// ---- the schema

export function lookupDoc(root: Dict, ptr: string): Dict {
  if (ptr === "/") return root;
  let node = root;
  for (let seg of ptr.replace(/^\//, "").split("/")) {
    seg = seg.replace(/~1/g, "/").replace(/~0/g, "~");
    if ("$ref" in node) {
      throw new SchemaError(`a field of a held pack: it is documented in that pack's ${DOCS_DIR}/`);
    }
    const props = node.properties as Dict | undefined;
    if (props === undefined) throw new SchemaError(`"${seg}": not an object`);
    if (!(seg in props)) throw new SchemaError(`"${seg}": no such field`);
    node = props[seg] as Dict;
  }
  return node;
}

/** The pack and its fields, depth first in the order of the schema; not into a held pack. */
export function* walkDoc(root: Dict): Generator<[string, Dict]> {
  function* rec(ptr: string, node: Dict): Generator<[string, Dict]> {
    yield [ptr, node];
    if ("$ref" in node) return;
    const prefix = ptr.endsWith("/") ? ptr.slice(0, -1) : ptr;
    const props = (node.properties ?? {}) as Dict;
    for (const [k, child] of Object.entries(props)) {
      yield* rec(`${prefix}/${k.replace(/~/g, "~0").replace(/\//g, "~1")}`, child as Dict);
    }
  }
  yield* rec("/", root);
}

function examplesOf(node: Dict, xs: string[]): unknown[] {
  if ("$ref" in node || node.format === "password" || node.writeOnly) {
    throw new SchemaError("a held pack and a secret have no examples");
  }
  const out: unknown[] = [];
  const t = node.type;
  for (const x of xs) {
    if (t === "string") {
      out.push(x);
    } else if (t === "integer" || t === "number") {
      if (INTEGER.test(x)) {
        const n = BigInt(x);
        if (n < -(1n << 63n) || n >= 1n << 63n) throw new SchemaError(`"${x}": ${NUMBER_RULE}`);
        out.push(Number(x));
      } else if (t === "number" && FRACTION.test(x) && canonicalFraction(x)) {
        out.push(Number(x)); // its shortest repr is x: at most 15 digits, between 0.0001 and 1e15
      } else {
        throw new SchemaError(`"${x}": ${NUMBER_RULE}`);
      }
    } else if (t === "boolean") {
      if (x !== "true" && x !== "false") throw new SchemaError(`"${x}" is not true or false`);
      out.push(x === "true");
    } else {
      throw new SchemaError("only a string, a number or a boolean has examples here");
    }
  }
  return out;
}

/** Write the documentation into a pack's schema, as the Go sdk's docs.Apply does. */
export function applyDocs(root: Dict, base: DocFile, others: DocFile[]): void {
  for (const f of [base, ...others]) {
    for (const [ptr, e] of f.fields) {
      try {
        const node = lookupDoc(root, ptr);
        if (f.base) {
          if (e.title) node.title = e.title;
          if (e.description) node.description = e.description;
          if (e.examples.length) node.examples = examplesOf(node, e.examples);
          if (e.group) node[KEY_GROUP] = e.group;
        } else {
          const t: Dict = {};
          if (e.description) t.description = e.description;
          if (e.title) t.title = e.title;
          if (Object.keys(t).length) {
            ((node[KEY_I18N] ??= {}) as Dict)[f.lang] = t;
          }
        }
      } catch (err) {
        if (err instanceof SchemaError) throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: ${ptr}: ${err.message}`);
        throw err;
      }
    }
  }
  const used: string[] = [];
  for (const [, node] of walkDoc(root)) {
    const g = node[KEY_GROUP];
    if (typeof g === "string" && g && !used.includes(g)) used.push(g);
  }
  const groups: Dict[] = [];
  for (const gid of used) {
    const title = base.groups[gid] ?? "";
    if (!title) {
      throw new SchemaError(`${DOCS_DIR}/${base.lang}.yaml: group "${gid}" has no title under groups:`);
    }
    const g: Dict = { id: gid, title };
    const tr: Dict = {};
    for (const f of others) if (f.groups[gid]) tr[f.lang] = { title: f.groups[gid] };
    if (Object.keys(tr).length) g[KEY_I18N] = tr;
    groups.push(g);
  }
  for (const gid of Object.keys(base.groups)) {
    if (!used.includes(gid)) {
      throw new SchemaError(`${DOCS_DIR}/${base.lang}.yaml: group "${gid}": no field is in it`);
    }
  }
  if (groups.length) root[KEY_GROUPS] = groups;
  const byId: Record<string, Dict> = {};
  for (const [, node] of walkDoc(root)) {
    for (const r of (node["x-pack-rules"] ?? []) as Dict[]) {
      if (r.id) byId[r.id as string] = r;
    }
  }
  for (const f of [base, ...others]) {
    for (const [rid, msg] of Object.entries(f.rules)) {
      const r = byId[rid];
      if (r === undefined) {
        throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: rules: "${rid}" is not the id of a rule of the pack`);
      }
      if (!msg) continue;
      if (f.base) {
        r.message = msg;
      } else {
        ((r[KEY_I18N] ??= {}) as Dict)[f.lang] = { message: msg };
      }
    }
  }
  const diags: Record<string, Dict> = {};
  for (const d of (root["x-pack-diagnose"] ?? []) as Dict[]) diags[d.id as string] = d;
  for (const f of [base, ...others]) {
    for (const [did, cause] of Object.entries(f.diagnose)) {
      const d = diags[did];
      if (d === undefined) {
        throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: diagnose: "${did}" is not the id of a diagnosis of the pack`);
      }
      if (!cause) continue;
      if (f.base) {
        d.cause = cause;
      } else {
        ((d[KEY_I18N] ??= {}) as Dict)[f.lang] = { cause };
      }
    }
  }
  const actions: Record<string, Dict> = {};
  for (const a of (root["x-pack-actions"] ?? []) as Dict[]) actions[a.name as string] = a;
  for (const f of [base, ...others]) {
    for (const [aid, text] of Object.entries(f.actions)) {
      const [name, param] = aid.split("/");
      const a = actions[name];
      if (a === undefined) {
        throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: actions: "${name}" is not an action of the pack`);
      }
      if (!text) continue;
      if (!param) {
        if (f.base) {
          a.description = text;
        } else {
          ((a[KEY_I18N] ??= {}) as Dict)[f.lang] = { description: text };
        }
        reorder(a, ACTION_ORDER);
        continue;
      }
      const props = ((a.params as Dict | undefined)?.properties ?? {}) as Dict;
      if (!(param in props)) {
        throw new SchemaError(`${DOCS_DIR}/${f.lang}.yaml: actions: "${param}" is not a parameter of ${name}`);
      }
      const p = props[param] as Dict;
      if (f.base) {
        p.description = text;
      } else {
        ((p[KEY_I18N] ??= {}) as Dict)[f.lang] = { description: text };
      }
      reorder(p, PARAM_ORDER);
    }
  }
  root[KEY_LANGS] = [base.lang, ...others.map((f) => f.lang)];
}

function reorder(d: Dict, order: string[]): void {
  // d's keys, in place, in the order the Go sdk writes them; a language added to x-pack-i18n sorted among the others.
  if (KEY_I18N in d) {
    const i18n = d[KEY_I18N] as Dict;
    d[KEY_I18N] = Object.fromEntries(Object.keys(i18n).sort().map((k) => [k, i18n[k]]));
  }
  const items = ordered(d, order);
  for (const k of Object.keys(d)) delete d[k];
  Object.assign(d, items);
}
