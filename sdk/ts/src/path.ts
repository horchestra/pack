// The names of a pack's fields in formulas and rules, found from the fields themselves: the author writes none of
// them by hand, so a renamed json name or a moved field moves them along.
//
//     pointer(o => o.bao.storage)   ->  "/openbao/storage"   the key of packCompute / packRules
//     dotted(o => o.postgres)       ->  "postgres"            as a formula reads it: pack.values.postgres

import { fieldsOf, SchemaError } from "./reflect.ts";

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;

const TRACE = Symbol("trace");

type Ctor = new () => object;

function makeTrace(ctor: Ctor | null, names: string[]): object {
  return new Proxy({}, {
    get(_, prop) {
      if (prop === TRACE) return { names };
      if (typeof prop !== "string") return undefined;
      if (ctor === null) {
        throw new SchemaError(`${names.join(".") || "the pack"} has no fields: "${prop}"`);
      }
      const inst = new ctor();
      for (const f of fieldsOf(inst)) {
        if (f.key === prop) {
          if (!f.json) throw new SchemaError(`"${prop}" is not in the schema (skipped)`);
          return makeTrace(f.pack ?? f.ctor ?? null, [...names, f.json]);
        }
      }
      throw new SchemaError(`${ctor.name} has no field "${prop}"`);
    },
  });
}

function names(root: object, select: (o: any) => unknown): string[] {
  const ctor = root.constructor as Ctor;
  const end = select(makeTrace(ctor, []));
  const t = (end as Record<symbol, { names: string[] } | undefined>)?.[TRACE];
  if (!t || !t.names.length) throw new SchemaError("select a field of the pack: o => o.some.field");
  return t.names;
}

/** The JSON Pointer of a field of root. */
export function pointer(root: object, select: (o: any) => unknown): string {
  return names(root, select).map((n) => "/" + n.replace(/~/g, "~0").replace(/\//g, "~1")).join("");
}

/** The path of a field the way a formula or a rule reads it; a name a formula cannot select is refused. */
export function dotted(root: object, select: (o: any) => unknown): string {
  const ns = names(root, select);
  for (const n of ns) {
    if (!IDENT.test(n)) throw new SchemaError(`"${n}" is not a name a formula can select`);
  }
  return ns.join(".");
}
