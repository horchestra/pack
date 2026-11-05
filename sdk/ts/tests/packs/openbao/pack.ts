// openbao, which holds tembo, the pack of the Go tests (sdk/internal/testpacks/openbao), written in TypeScript.

import { dotted, field, Pack, pointer } from "../../../src/index.ts";
import type { Meta, Root, Rule } from "../../../src/index.ts";
import { Tembo } from "../tembo/pack.ts";

class StorageBlock {
  type = field("");
  config = field({}, { omitempty: true });
}

class Config {
  ui = field(false, { omitempty: true });
  logLevel = field("", { json: "log_level", omitempty: true, enum: ["trace", "debug", "info", "warn", "error"] });
  apiAddr = field("", { json: "api_addr" });
  storage = field([] as StorageBlock[], { omitempty: true, of: StorageBlock });
}

const VERSION = "2.1.0";
const IMAGE = "images/openbao:2.1.0";

export class OpenBao extends Pack {
  bao = field(new Config(), { json: "openbao" });
  postgres = field(null as Tembo | null, { pack: Tembo, omitempty: true });

  setDefaults(): void {
    this.bao.logLevel = "info";
  }

  // The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
  // postgres and wrote no storage himself.
  packCompute(): Record<string, string> {
    const pg = dotted(this, (o) => o.postgres);
    return {
      [pointer(this, (o) => o.bao.storage)]:
        `has(pack.values.${pg}) ? [{'type': 'postgresql', 'config': {'connection_url': ` +
        `'postgres://' + pack.keys.${pg}.service + ':5432/openbao'}}] : null`,
    };
  }

  // ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's.
  packRules(): Record<string, Rule[]> {
    const pg = dotted(this, (o) => o.postgres);
    const storage = dotted(this, (o) => o.bao.storage);
    return {
      [pointer(this, (o) => o.bao.storage)]: [{
        id: "storage-from-postgres",
        rule: `!has(pack.values.${pg}) || !has(values.${storage}) || size(values.${storage}) == 0`,
        message: "storage is filled from the embedded postgres and must not be set",
      }],
    };
  }

  meta(): Meta {
    return {
      name: "openbao", version: VERSION, description: "secrets", license: "MPL-2.0", image: IMAGE,
      annotations: { "example.org/tier": "core" },
    };
  }

  packRoot(): Root {
    return {
      ports: { api: 8200 },
      content: [{ path: "/etc/openbao/config.json", values: "openbao" }],
      readiness: { http: "api/v1/sys/health" },
      savepoints: [dotted(this, (o) => o.postgres)],
    };
  }
}
