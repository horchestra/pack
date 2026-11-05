// packsdk: the author's side of pack. A pack is a class whose fields, declared with field(), are the application's
// settings; generate() writes its schema.json, pack.yaml and templates/ — the artifacts the Go sdk writes, byte for
// byte the same format. Run by node directly (type stripping); no dependencies.
//
//     import { field, generate, Meta, Pack, Root } from "packsdk";
//
//     class App extends Pack {
//       apiAddr = field("", { json: "api_addr" });
//       meta() { return { name: "app", version: "1.0.0", image: "images/app:1.0.0" }; }
//       packRoot() { return { ports: { api: 8200 }, readiness: { http: "api/health" } }; }
//     }
//
//     generate("out", new App());

export { Field, field, fieldsOf, initFields, isZero, leafWithDefault, Pack, prune, reflect, SchemaError, toJson, typeSchema } from "./reflect.ts";
export type { FieldOpts, Spec } from "./reflect.ts";
export { dotted, pointer } from "./path.ts";
export {
  ANNOTATION_IMAGE, annotations, ARTIFACT_PACK, ARTIFACT_SCHEMA, ARTIFACT_TEMPLATE, CONTENT_DIR, content,
  DEFAULT_REPO, generate, generateIn, main, PACK_FILE, packDir, packFile, packFileYaml, packs, SCHEMA_FILE, schema,
  TEMPLATES_DIR,
} from "./pack.ts";
export { dumpYaml } from "./yaml.ts";
export { Secret } from "./vocab.ts";
export type {
  Action, Diagnosis, Fix, Hook, Meta, OSPackages, PackFile, Param, Readiness, Rollout, Root, Rule, Step, Suggest,
  Upgrade, Volume, When,
} from "./vocab.ts";
