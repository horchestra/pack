// tembo, the pack of the Go tests (sdk/internal/testpacks/tembo), written in TypeScript.

import { field, Pack, pointer, Secret } from "../../../src/index.ts";
import type { Action, Diagnosis, Meta, Root, Suggest } from "../../../src/index.ts";

/** The node's tags and the log it keeps. */
class Node {
  raftRetentionLogSize = field(0, { json: "raft_retention_log_size", omitempty: true });
  nofailover = field(false, { omitempty: true });
  nosync = field(false, { omitempty: true });
  failoverPriority = field(0, { json: "failover_priority", omitempty: true });
}

export class Tembo extends Pack {
  id = field("", { env: "TEMBO_ID", omitempty: true, pattern: "^[a-z0-9_]*$", maxLength: 63 });
  listen = field("", { env: "TEMBO_LISTEN", omitempty: true });
  listenRo = field("", { json: "listen_ro", env: "TEMBO_LISTEN_RO", omitempty: true });
  db = field("", { env: "TEMBO_DB", omitempty: true });
  pgData = field("", { json: "pg_data", env: "TEMBO_PG_DATA", omitempty: true });
  pgPort = field(0, { json: "pg_port", env: "TEMBO_PG_PORT", omitempty: true, minimum: 1, maximum: 65535 });
  pgPassword = field(new Secret(), { json: "pg_password", env: "TEMBO_PG_PASSWORD", omitempty: true });
  config = field("", { env: "TEMBO_CONFIG", omitempty: true });
  node = field(new Node(), { omitempty: true });

  setDefaults(): void {
    this.listen = ":5432";
    this.listenRo = ":5434";
    this.db = "/var/lib/postgresql/tembo.db";
    this.pgData = "/var/lib/postgresql/data";
    this.pgPort = 5433;
    this.config = "/etc/tembo/tembo.env";
    this.node.raftRetentionLogSize = 8 << 20;
  }

  meta(): Meta {
    return {
      name: "tembo", version: "0.2.0", description: "PostgreSQL with raft", license: "Apache-2.0",
      source: "https://example.org/tembo", image: "images/tembo:0.2.0",
    };
  }

  // the port next to the agent's own when a host of the target already listens on it
  packSuggest(): Record<string, Suggest> {
    return {
      [pointer(this, (o) => o.pgPort)]: {
        expr: "has(facts.hosts) && facts.hosts.exists(h, has(facts.hosts[h].ports) && facts.hosts[h].ports.exists(p, p.port == 5433)) ? 5434 : null",
        why: "5433 is taken on a host",
      },
    };
  }

  // postgres that cannot listen because something on the host already does
  packActions(): Action[] {
    return [
      {
        name: "backup", command: ["tembo", "backup", "--to", "{{ .to }}", "--format", "{{ .format }}"],
        timeout: "30m",
        params: [
          { name: "to", type: "string", required: true },
          { name: "format", type: "string", default: "custom", enum: ["custom", "plain"] },
        ],
      },
      {
        name: "restore", command: ["tembo", "restore", "--from", "{{ .from }}"], mutating: true, approve: true,
        params: [{ name: "from", type: "string", required: true }],
      },
    ];
  }

  packDiagnose(): Diagnosis[] {
    return [{
      id: "port-taken", match: "address already in use.*:5433|could not bind .*5433",
      cause: "postgres cannot listen: something on the host already listens on 5433",
      fix: [
        { patch: pointer(this, (o) => o.pgPort), expr: "5434", why: "the port next to it" },
        { command: "pack discover", why: "see what listens on the hosts" },
      ],
    }];
  }

  packRoot(): Root {
    return {
      ports: { api: 8001, pg: 5432, "pg-ro": 5434 },
      content: [{ path: "/etc/tembo/tembo.env", template: "tembo.env.ptmpl", values: "node", parseAs: "text", mode: "0640" }],
      data: { data: { mountPath: "/var/lib/postgresql", size: "10Gi" } },
      minimum: { memory: "1Gi" },
      command: ["tembo", "run"],
      readiness: { http: "api/health" },
    };
  }
}
