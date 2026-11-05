// Reflect: a pack class into its JSON Schema, the way the Go sdk does it. The schema is the application's own
// configuration: the properties are the class's fields, declared with field(); a field without omitempty is
// required; the value after setDefaults is the defaults document. A field declared with pack: becomes a reference
// to that pack's schema.

import {
  actionJson, diagnosisJson, PACK_SCHEME, PARAM_TYPES, rootJson, ruleJson, Secret, secretJson, suggestJson,
} from "./vocab.ts";
import type { Action, Diagnosis, Rule, Suggest } from "./vocab.ts";

export const SCHEMA = "https://json-schema.org/draft/2020-12/schema";

const ACTION_NAME = /^[a-z][a-z0-9-]*$/;
const PARAM_NAME = /^[a-z][a-z0-9_]*$/;

type Dict = Record<string, unknown>;

export class SchemaError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SchemaError";
  }
}

const PACK_STACK = new WeakMap<Pack, string>();

/** What a pack is: the author's side, subclassed. */
export abstract class Pack {
  constructor() {
    // kept for packDir: the directory of the file the pack's class is defined in is in this stack
    PACK_STACK.set(this, new Error().stack ?? "");
  }
  /** What the zero values cannot say; sets, never adds (it may run twice). */
  setDefaults(): void {}  /** The identity: name, version, image. */
  abstract meta(): import("./vocab.ts").Meta;
  /** How it runs, upgrades and rolls back: ports, volumes, readiness, hooks, savepoints … */
  abstract packRoot(): import("./vocab.ts").Root;
  /** Optional: {pointer: CEL formula} that fills a value. */
  packCompute(): Record<string, string> { return {}; }
  /** Optional: {pointer: [Rule, …]} that the values must satisfy. */
  packRules(): Record<string, Rule[]> { return {}; }
  packSuggest(): Record<string, Suggest> { return {}; }
  packDiagnose(): Diagnosis[] { return []; }
  /** Optional: the operations the author names, pack run performs (x-pack-actions). */
  packActions(): Action[] { return []; }
}

type PackCtor = new () => Pack;

/** The item type of an array field, or the value type of a map field: a scalar's name, "any", the Secret class, or a
 * struct class. */
export type Spec = "string" | "integer" | "number" | "boolean" | "any" | (new () => object);

export interface FieldOpts {
  json?: string;      // the property's name in the schema (the property's name by default); the application's own key
  env?: string;       // the field is the environment variable NAME
  readOnly?: boolean; // the operator does not set it (the Go jsonschema tag's readOnly=true)
  omitempty?: boolean; // the field is optional; without it the field is required (the Go json tag's omitempty)
  skip?: boolean;     // not a setting (json:"-")
  enum?: unknown[];
  pattern?: string;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  number?: boolean;   // a number default that is a whole number is an integer; say number: true when it is not
  of?: Spec;          // the item type of an array field
  values?: Spec;      // the value type of a map field
  pack?: PackCtor;    // the field holds a pack of its own (null default makes it optional)
}

/** The sentinel a class field holds until fieldsOf unwraps it into the default value. */
export class Field {
  readonly def: unknown;
  readonly factory: boolean;
  readonly opts: FieldOpts;
  constructor(def: unknown, factory: boolean, opts: FieldOpts) {
    this.def = def;
    this.factory = factory;
    this.opts = opts;
  }
}

/** One setting of the application. The default is the zero value, or a factory of it (for a struct, an array that
 * starts non-empty); setDefaults sets the rest. A function default is the factory. */
export function field(def: unknown, opts: FieldOpts = {}): any {
  return new Field(def, typeof def === "function", opts);
}

interface FieldMeta {
  json: string;       // the property's name; "" when skipped
  opts: FieldOpts;
  pack?: PackCtor;    // the class of a held pack
  ctor?: (new () => object); // the class of a struct value
}

interface FieldInfo extends FieldMeta {
  key: string;
  value: unknown;
}

const META = new WeakMap<object, Map<string, FieldMeta>>();

function isStructInstance(v: unknown): v is object {
  if (typeof v !== "object" || v === null) return false;
  if (v instanceof Secret || v instanceof Field || v instanceof Pack) return false;
  if (Array.isArray(v)) return false;
  return (v as object).constructor !== Object;
}

function metaOf(inst: object): Map<string, FieldMeta> {
  let m = META.get(inst);
  if (m) return m;
  m = new Map();
  for (const key of Object.keys(inst)) {
    let v = (inst as Dict)[key];
    let opts: FieldOpts = {};
    if (v instanceof Field) {
      opts = v.opts;
      v = v.factory ? (v.def as () => unknown)() : v.def;
      (inst as Dict)[key] = v; // unwrap: from now on the author sees plain values
    }
    const fm: FieldMeta = { json: opts.skip || opts.json === "-" ? "" : (opts.json ?? key), opts };
    if (opts.pack) fm.pack = opts.pack;
    else if (v instanceof Pack) fm.pack = v.constructor as PackCtor;
    else if (isStructInstance(v)) fm.ctor = (v as object).constructor as new () => object;
    m.set(key, fm);
  }
  META.set(inst, m);
  return m;
}

/** The fields of a pack or a struct, unwrapped and in the order of the class. */
export function fieldsOf(inst: object): FieldInfo[] {
  const m = metaOf(inst);
  return [...m.entries()].map(([key, fm]) => ({ key, ...fm, value: (inst as Dict)[key] }));
}

/** Runs the field machinery of a pack and of the structs inside it, so that setDefaults sees plain values. */
export function initFields(inst: object): void {
  for (const f of fieldsOf(inst)) {
    if (isStructInstance(f.value)) initFields(f.value);
  }
}

// ---- values

export function isZero(v: unknown): boolean {
  if (v === null || v === undefined) return true;
  if (typeof v === "string") return v.length === 0;
  if (typeof v === "boolean") return !v;
  if (typeof v === "number") return v === 0;
  if (Array.isArray(v)) return v.length === 0;
  if (v instanceof Secret) return !(v.name || v.key);
  if (isStructInstance(v) || v instanceof Pack) {
    return fieldsOf(v).every((f) => !f.json || isZero(f.value));
  }
  if (typeof v === "object") return Object.keys(v as Dict).length === 0;
  return false;
}

/** encoding/json of a value: every field but the optional zero ones. */
export function toJson(v: unknown): unknown {
  if (v instanceof Secret) return secretJson(v);
  if (isStructInstance(v) || v instanceof Pack) {
    const out: Dict = {};
    for (const f of fieldsOf(v)) {
      if (!f.json) continue;
      if (f.opts.omitempty && isZero(f.value)) continue;
      out[f.json] = toJson(f.value);
    }
    return out;
  }
  if (Array.isArray(v)) return v.map(toJson);
  if (typeof v === "object" && v !== null) {
    return Object.fromEntries(Object.entries(v as Dict).map(([k, x]) => [k, toJson(x)]));
  }
  return v;
}

/** Drop zero values — "", 0, false, null, empty objects and arrays — from a marshalled value. */
export function prune(v: unknown): unknown {
  return pruneValue(toJson(v))[0];
}

function pruneValue(v: unknown): [unknown, boolean] {
  if (typeof v === "object" && v !== null && !Array.isArray(v)) {
    const out: Dict = {};
    for (const [k, e] of Object.entries(v as Dict)) {
      const [pe, keep] = pruneValue(e);
      if (keep) out[k] = pe;
    }
    return [out, Object.keys(out).length > 0];
  }
  if (Array.isArray(v)) return [v, v.length > 0];
  if (typeof v === "string") return [v, v.length > 0];
  if (typeof v === "boolean") return [v, v];
  if (typeof v === "number") return [v, v !== 0];
  if (v === null || v === undefined) return [null, false];
  return [v, true];
}

/** A field set to something is a setting with that default; a struct is not one itself — its fields are —
 * and neither is a secret. */
export function leafWithDefault(v: unknown): boolean {
  if (v === null || v === undefined || v instanceof Secret || isZero(v)) return false;
  return !isStructInstance(v) && !(v instanceof Pack);
}

// ---- types

function specSchema(spec: Spec): Dict {
  if (spec === "string") return { type: "string" };
  if (spec === "boolean") return { type: "boolean" };
  if (spec === "integer") return { type: "integer" };
  if (spec === "number") return { type: "number" };
  if (spec === "any") return {};
  if ((spec as unknown) === Secret) return { type: "string", format: "password" };
  if (isPackCtor(spec as new () => object)) {
    throw new SchemaError("a pack is held by a field of the pack itself, not of a list or a map");
  }
  return structSchema(new (spec as new () => object)());
}

function isPackCtor(c: unknown): boolean {
  return typeof c === "function" && (c === Pack || (c as Function).prototype instanceof Pack);
}

function structSchema(inst: object): Dict {
  const props: Dict = {};
  const required: string[] = [];
  for (const f of fieldsOf(inst)) {
    if (!f.json) continue;
    props[f.json] = f.pack ? {} : typeSchema(f.value, f.opts);
    if (!f.opts.omitempty) required.push(f.json);
  }
  const out: Dict = { properties: props, additionalProperties: false, type: "object" };
  if (required.length) out.required = required;
  return out;
}

export function typeSchema(value: unknown, opts: FieldOpts): Dict {
  if (value instanceof Secret) return { type: "string", format: "password" };
  if (typeof value === "boolean") return { type: "boolean" };
  if (typeof value === "number") {
    return { type: opts.number || !Number.isInteger(value) ? "number" : "integer" };
  }
  if (typeof value === "string") return { type: "string" };
  if (Array.isArray(value)) return { items: opts.of ? specSchema(opts.of) : {}, type: "array" };
  if (isStructInstance(value)) return structSchema(value);
  if (typeof value === "object" && value !== null) {
    return opts.values ? { type: "object", additionalProperties: specSchema(opts.values) } : { type: "object" };
  }
  throw new SchemaError(`no JSON Schema for the type of ${String(value)}`);
}

// ---- the schema

/** The schema of a pack: setDefaults runs on the value first. */
export function reflect(p: Pack): Dict {
  initFields(p);
  p.setDefaults();
  const s: Dict = { $schema: SCHEMA, ...structSchema(p) };
  annotate(s, p, "");
  s["x-pack"] = rootJsonOf(p);
  for (const [ptr, expr] of Object.entries(p.packCompute())) {
    try {
      lookup(s, ptr)["x-pack-compute"] = expr;
    } catch (e) {
      if (e instanceof SchemaError) throw new SchemaError(`x-pack-compute ${ptr}: ${e.message}`);
      throw e;
    }
  }
  const diags = p.packDiagnose();
  const seenDiag = new Set<string>();
  for (const d of diags) {
    if (!d.id || !d.match || !d.cause) {
      throw new SchemaError(`x-pack-diagnose: a diagnosis needs an id, a match and a cause: ${JSON.stringify(d)}`);
    }
    if (seenDiag.has(d.id)) throw new SchemaError(`x-pack-diagnose: the id "${d.id}" twice`);
    seenDiag.add(d.id);
    d.fix?.forEach((fx, i) => {
      if (!!fx.patch === !!fx.command || (fx.patch && !fx.expr)) {
        throw new SchemaError(`x-pack-diagnose ${d.id}: fix ${i}: a patch with its expr, or a command`);
      }
      if (fx.patch) {
        try {
          lookup(s, fx.patch);
        } catch (e) {
          if (e instanceof SchemaError) {
            throw new SchemaError(`x-pack-diagnose ${d.id}: fix ${i}: ${fx.patch}: ${e.message}`);
          }
          throw e;
        }
      }
    });
  }
  if (diags.length) s["x-pack-diagnose"] = diags.map(diagnosisJson);
  const actions = p.packActions();
  const seenAction = new Set<string>();
  for (const a of actions) {
    if (!ACTION_NAME.test(a.name)) {
      throw new SchemaError(`x-pack-actions: "${a.name}": a name of lower case letters, digits and dashes`);
    }
    if (!a.command?.length) throw new SchemaError(`x-pack-actions ${a.name}: no command`);
    if (seenAction.has(a.name)) throw new SchemaError(`x-pack-actions: the name "${a.name}" twice`);
    seenAction.add(a.name);
    const names = new Set<string>();
    for (const pa of a.params ?? []) {
      if (!PARAM_NAME.test(pa.name)) {
        throw new SchemaError(
          `x-pack-actions ${a.name}: parameter "${pa.name}": a name of lower case letters, digits and underscores`);
      }
      if (names.has(pa.name)) {
        throw new SchemaError(`x-pack-actions ${a.name}: parameter "${pa.name}" twice`);
      }
      names.add(pa.name);
      if (!PARAM_TYPES.includes(pa.type)) {
        throw new SchemaError(`x-pack-actions ${a.name}: parameter ${pa.name}: type is one of ${PARAM_TYPES.join(", ")}`);
      }
    }
  }
  if (actions.length) s["x-pack-actions"] = actions.map(actionJson);
  for (const [ptr, sg] of Object.entries(p.packSuggest())) {
    if (!sg.expr) throw new SchemaError(`x-pack-suggest ${ptr}: no expression`);
    try {
      lookup(s, ptr)["x-pack-suggest"] = suggestJson(sg);
    } catch (e) {
      if (e instanceof SchemaError) throw new SchemaError(`x-pack-suggest ${ptr}: ${e.message}`);
      throw e;
    }
  }
  const ids: Record<string, string> = {};
  for (const [ptr, rules] of Object.entries(p.packRules())) {
    for (const r of rules) {
      if (r.id && ids[r.id]) {
        throw new SchemaError(`x-pack-rules: the id "${r.id}" is of a rule on ${ids[r.id]} and on ${ptr}`);
      }
      if (r.id) ids[r.id] = ptr;
    }
    try {
      lookup(s, ptr)["x-pack-rules"] = rules.map(ruleJson);
    } catch (e) {
      if (e instanceof SchemaError) throw new SchemaError(`x-pack-rules ${ptr}: ${e.message}`);
      throw e;
    }
  }
  return s;
}

// x-pack of the root
function rootJsonOf(p: Pack): Dict {
  return rootJson(p.packRoot());
}

function annotate(node: Dict, value: object, path: string): void {
  // Defaults, files and environment variables onto the node of a struct. A struct behind a pointer that is not set
  // (value is null) has none of them, as in the Go sdk.
  if (value === null || value === undefined) return;
  for (const f of fieldsOf(value)) {
    if (!f.json) continue;
    if (f.pack) {
      if (path) {
        throw new SchemaError(
          `${path}/${f.json}: a pack is held by a field of the pack itself, not of a struct inside it`);
      }
      const ref: Dict = { $ref: `${PACK_SCHEME}${new f.pack().meta().name}#` };
      if (f.value !== null && f.value !== undefined && !isZero(f.value)) ref.default = prune(f.value);
      (node.properties as Dict)[f.json] = ref;
      continue;
    }
    const prop = (node.properties as Dict)[f.json] as Dict;
    if (leafWithDefault(f.value)) prop.default = toJson(f.value);
    if (f.opts.readOnly) prop.readOnly = true;
    constrain(prop, f.opts, `${path}/${f.json}`);
    const env = f.opts.env;
    if (env && env !== "-") prop["x-pack-env"] = env;
    if (f.ctor) annotate(prop, f.value as object, `${path}/${f.json}`);
  }
}

// what a value may be: the field's argument, and the keyword of the schema it is
const CONSTRAINTS: [keyof FieldOpts, string][] = [
  ["enum", "enum"], ["pattern", "pattern"], ["minimum", "minimum"], ["maximum", "maximum"],
  ["minLength", "minLength"], ["maxLength", "maxLength"],
];

// the schema types each constraint is of
const OF: Record<string, string[]> = {
  enum: ["string", "integer", "number"], pattern: ["string"], minLength: ["string"], maxLength: ["string"],
  minimum: ["integer", "number"], maximum: ["integer", "number"],
};

function isOfType(v: unknown, typ: string): boolean {
  if (typeof v === "boolean") return typ === "boolean";
  switch (typ) {
    case "string": return typeof v === "string";
    case "integer": return typeof v === "number" && Number.isInteger(v);
    case "number": return typeof v === "number";
    default: return false;
  }
}

function constrain(prop: Dict, opts: FieldOpts, where: string): void {
  // What a value of the setting may be, onto its node: a constraint its type has not, or an enum value of another
  // type, is refused (a schema no value passes), as the Java sdk refuses it.
  const typ = prop.type as string | undefined;
  for (const [arg, key] of CONSTRAINTS) {
    let v = opts[arg] as unknown;
    if (v === undefined || v === null) continue;
    if (!typ || !OF[arg].includes(typ) || prop.format === "password") {
      throw new SchemaError(`${where}: ${arg} is of ${OF[arg].join(" or ")}, not of this ${typ}`);
    }
    if (arg === "enum") {
      const bad = (v as unknown[]).filter((x) => !isOfType(x, typ));
      if (bad.length) throw new SchemaError(`${where}: enum ${JSON.stringify(bad)} is not of type ${typ}`);
      v = [...(v as unknown[])];
    } else if ((arg === "minLength" || arg === "maxLength")
               && (typeof v !== "number" || !Number.isInteger(v) || v < 0)) {
      throw new SchemaError(`${where}: ${arg} ${JSON.stringify(v)} is not a length`);
    } else if ((arg === "minimum" || arg === "maximum") && !isOfType(v, "number")) {
      throw new SchemaError(`${where}: ${arg} ${JSON.stringify(v)} is not a number`);
    }
    prop[key] = v;
  }
}

/** The stack captured at the pack's construction; packDir reads the author's directory from it. */
export function packStack(p: Pack): string {
  return PACK_STACK.get(p) ?? "";
}

export function lookup(s: Dict, ptr: string): Dict {  let node = s;
  for (const seg of ptr.replace(/^\//, "").split("/")) {
    const props = node.properties as Dict | undefined;
    if (props === undefined) throw new SchemaError(`"${seg}": not an object`);
    if (!(seg in props)) throw new SchemaError(`"${seg}": no such property`);
    node = props[seg] as Dict;
  }
  return node;
}
