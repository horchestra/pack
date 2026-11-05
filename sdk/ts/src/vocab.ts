// The vocabulary a pack speaks on top of JSON Schema: x-pack (Root, its Content of File), x-pack-rules (Rule), and
// Meta, everything about a pack that goes into its OCI artifact. The same words as the Go sdk, one to one.

export const PHASES = ["preInstall", "postInstall", "preUpgrade", "postUpgrade", "preDowngrade", "postDowngrade"];
export const RULE_PHASES = ["preMutate", "postMutate"];
export const FORMATS = ["json", "yaml", "properties", "env", "shell", "ini", "flagfile", "lines", "hadoop-xml", "toml", "text"];
export const FORMAT_SECRET = "secret";
export const PACK_SCHEME = "pack://";
export const PARAM_TYPES = ["string", "integer", "number", "boolean"];

type Dict = Record<string, unknown>;

// omitempty: what is empty is not written.
export function drop(d: Dict): Dict {
  const out: Dict = {};
  for (const [k, v] of Object.entries(d)) {
    if (v === undefined || v === null || v === "" || v === 0 || v === false) continue;
    if (Array.isArray(v) && v.length === 0) continue;
    if (typeof v === "object" && !Array.isArray(v) && Object.keys(v as Dict).length === 0) continue;
    out[k] = v;
  }
  return out;
}

/** A volume the component keeps: where it is mounted is the component's fact, how big is the operator's. */
export interface Volume {
  mountPath: string;
  size?: string;
}

export function volumeJson(v: Volume): Dict {
  return { mountPath: v.mountPath, ...drop({ size: v.size }) };
}

export interface Readiness {
  tcp?: string;   // a port name
  http?: string;  // "<port>/<path>"
  exec?: string[];
}

export function readinessJson(r: Readiness): Dict {
  return drop({ tcp: r.tcp, http: r.http, exec: r.exec });
}

export interface Upgrade {
  from: string; // version constraint on the version being upgraded from
}

export function upgradeJson(u: Upgrade): Dict {
  return { from: u.from };
}

export interface When {
  from?: string;
  to?: string;
}

export function whenJson(w: When): Dict {
  return drop({ from: w.from, to: w.to });
}

export interface Step {
  command: string[];
  image?: string; // new | old
}

export function stepJson(s: Step): Dict {
  return { ...drop({ image: s.image }), command: [...s.command] };
}

/** One step the component contributes to the saga of its own installation or upgrade. */
export interface Hook {
  name: string;
  phase: string;
  command: string[];
  when?: When;
  image?: string;
  exclusive?: boolean;
  timeout?: string;
  idempotent?: boolean;
  compensate?: Step;
}

export function hookJson(h: Hook): Dict {
  const out: Dict = { name: h.name, phase: h.phase };
  if (h.when) out.when = whenJson(h.when);
  Object.assign(out, drop({ image: h.image }));
  out.command = [...h.command];
  Object.assign(out, drop({ exclusive: h.exclusive, timeout: h.timeout, idempotent: h.idempotent }));
  if (h.compensate) out.compensate = stepJson(h.compensate);
  return out;
}

export interface Rollout {
  maxUnavailable?: number;
}

export function rolloutJson(r: Rollout): Dict {
  return drop({ maxUnavailable: r.maxUnavailable });
}

/** A file in the component's containers, at path: from source (a file of content/, byte for byte), template (a file
 * of content/ rendered over the values the pack sees; with values, only when that value is there), or values (a
 * field's subtree, "conf", written in format, the name of path saying it when there is none). */
export interface PackFile {
  path: string;
  source?: string;
  template?: string;
  url?: string;
  values?: string;
  format?: string;
  parseAs?: string;
  mode?: string;
  perInstance?: boolean;
}

export function fileJson(f: PackFile): Dict {
  return drop({
    path: f.path, source: f.source, template: f.template, url: f.url, values: f.values,
    format: f.format, parseAs: f.parseAs, mode: f.mode, perInstance: f.perInstance,
  });
}

/** The installation on one OS, in this order: prepare installed, repo delivered, names installed, commands run,
 * content delivered, services started. A name or the url of a file is a template too; the target resolves
 * ${os:codename}, ${os:version} and ${os:arch}. */
export interface OSPackages {
  names: string[];
  prepare?: string[];
  repo?: PackFile[];
  commands?: string[][];
  content?: PackFile[];
  services?: string[];
}

export function osPackagesJson(p: OSPackages): Dict {
  const out: Dict = {};
  if (p.prepare?.length) out.prepare = [...p.prepare];
  if (p.repo?.length) out.repo = p.repo.map(fileJson);
  out.names = [...p.names];
  if (p.commands?.length) out.commands = p.commands.map((c) => [...c]);
  if (p.content?.length) out.content = p.content.map(fileJson);
  if (p.services?.length) out.services = [...p.services];
  return out;
}

/** x-pack on the root of a pack's schema: what the component is to the system around it. */
export interface Root {
  ports?: Record<string, number>;
  data?: Record<string, Volume>;
  content?: PackFile[];
  packages?: Record<string, OSPackages>; // by OS: the key is the target's to match
  minimum?: Record<string, string>;
  readiness?: Readiness;
  upgrade?: Upgrade;
  hooks?: Hook[];
  command?: string[]; // replaces the image's entrypoint+cmd wholesale
  restartPolicy?: string; // Always | OnFailure | Never
  savepoints?: string[];
  rollout?: Rollout;
}

export function rootJson(r: Root): Dict {
  const out: Dict = {};
  if (r.ports && Object.keys(r.ports).length) out.ports = { ...r.ports };
  if (r.data && Object.keys(r.data).length) {
    out.data = Object.fromEntries(Object.entries(r.data).map(([k, v]) => [k, volumeJson(v)]));
  }
  if (r.content?.length) out.content = r.content.map(fileJson);
  if (r.packages && Object.keys(r.packages).length) {
    out.packages = Object.fromEntries(
      Object.keys(r.packages).sort().map((k) => [k, osPackagesJson(r.packages![k])]));
  }
  if (r.minimum && Object.keys(r.minimum).length) out.minimum = { ...r.minimum };
  if (r.readiness) out.readiness = readinessJson(r.readiness);
  if (r.upgrade) out.upgrade = upgradeJson(r.upgrade);
  if (r.hooks?.length) out.hooks = r.hooks.map(hookJson);
  if (r.command?.length) out.command = [...r.command];
  if (r.restartPolicy) out.restartPolicy = r.restartPolicy;
  if (r.savepoints?.length) out.savepoints = [...r.savepoints];
  if (r.rollout) out.rollout = rolloutJson(r.rollout);
  return out;
}

/** A way out of a failure: a value for a setting (patch, its pointer, and expr, CEL) or a command to run. */
export interface Fix {
  patch?: string;
  expr?: string;
  command?: string;
  why?: string;
}

export function fixJson(f: Fix): Dict {
  return drop({ patch: f.patch, expr: f.expr, command: f.command, why: f.why });
}

/** One of x-pack-diagnose on the root: what a failure looks like (match, RE2 over the step's error and logs), what
 * it is (cause), the ways out (fix). */
export interface Diagnosis {
  id: string;
  match: string;
  cause: string;
  fix?: Fix[];
}

export function diagnosisJson(d: Diagnosis): Dict {
  return { id: d.id, match: d.match, cause: d.cause, ...drop({ fix: d.fix?.map(fixJson) }) };
}

export const ACTION_ORDER = ["name", "description", "command", "timeout", "mutating", "approve", "params", "x-pack-i18n"];
export const PARAM_ORDER = ["type", "description", "default", "enum", "x-pack-i18n"];

/** d with its keys in the order the Go sdk writes them. */
export function ordered(d: Dict, order: string[]): Dict {
  const out: Dict = {};
  for (const k of order) if (k in d) out[k] = d[k];
  return out;
}

/** A parameter of an action: a scalar of type (string, integer, number, boolean). */
export interface Param {
  name: string;
  type: string;
  required?: boolean;
  description?: string;
  default?: unknown;
  enum?: unknown[];
}

export function paramJson(p: Param): Dict {
  const out: Dict = { type: p.type, ...drop({ description: p.description }) };
  if (p.default !== undefined && p.default !== null) out.default = p.default;
  return { ...out, ...drop({ enum: p.enum ? [...p.enum] : undefined }) };
}

/** One of x-pack-actions on the root: an operation the author names, pack run performs in the first instance of a key
 * of the pack, as it runs. command's words are templates over the parameters ({{ .to }}); mutating — it changes the
 * installation; approve — never run unasked but under run.approve: auto. */
export interface Action {
  name: string;
  command: string[];
  description?: string;
  timeout?: string;
  mutating?: boolean;
  approve?: boolean;
  params?: Param[];
}

export function actionJson(a: Action): Dict {
  const out: Dict = {
    name: a.name, ...drop({ description: a.description }), command: [...a.command],
    ...drop({ timeout: a.timeout, mutating: a.mutating, approve: a.approve }),
  };
  if (a.params?.length) {
    const sorted = [...a.params].sort((x, y) => (x.name < y.name ? -1 : x.name > y.name ? 1 : 0));
    const ps: Dict = {
      type: "object",
      properties: Object.fromEntries(sorted.map((p) => [p.name, paramJson(p)])),
    };
    const req = sorted.filter((p) => p.required).map((p) => p.name);
    if (req.length) ps.required = req;
    ps.additionalProperties = false;
    out.params = ps;
  }
  return out;
}

/** x-pack-suggest on a node: a CEL expression pack fill offers the answer of for a setting not given, with why —
 * never a value by itself, as a formula's. */
export interface Suggest {
  expr: string;
  why?: string;
}

export function suggestJson(s: Suggest): Dict {
  return { expr: s.expr, ...drop({ why: s.why }) };
}

/** x-pack-rules on a node: a CEL expression that must hold of the values. id names it among the pack's rules, so
 * that docs/<lang>.yaml can say its message in other languages (rules: {<id>: …}). */
export interface Rule {
  rule: string;
  message?: string;
  phases?: string[];
  id?: string;
}

export function ruleJson(r: Rule): Dict {
  return { ...drop({ id: r.id }), rule: r.rule, ...drop({ message: r.message, phases: r.phases }) };
}

/** Everything about a pack that goes into its OCI artifact. name and version are required. */
export interface Meta {
  name: string; // a DNS label; the key of the artifact among the packs of a pack
  version: string;
  description?: string;
  documentation?: string;
  source?: string;
  url?: string;
  license?: string;
  vendor?: string;
  authors?: string;
  created?: string; // RFC 3339; empty — not written, so that the artifact is the same byte for byte
  image?: string; // repo:tag the application runs, a repository of the registry the pack is pushed to; "" — the pack runs nothing itself
  annotations?: Record<string, string>;
  dir?: string; // the pack's directory, whose content/ holds its files; "" — where the class is defined
}

/** A credential leaf: the operator gives a reference to a Secret, never a value. */
export class Secret {
  name: string;
  key: string;
  constructor(name = "", key = "") {
    this.name = name;
    this.key = key;
  }
}

export function secretJson(s: Secret): Dict {
  return s.name || s.key ? { secretRef: { name: s.name, key: s.key } } : {};
}
