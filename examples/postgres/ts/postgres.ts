// postgres: a single-instance PostgreSQL with an install hook (the extensions every database gets, after the first
// start) and a major-version upgrade hook (the saga runs pg_upgrade with the component stopped, and takes it back if
// it fails). The same pack is written in Go, Python and Java next to this one; all four generate the same artifacts
// (examples/postgres/golden).
//
//     node postgres.ts generate -o out

import { pathToFileURL } from "node:url";

import { field, main, Pack, pointer, Secret } from "../../../sdk/ts/src/index.ts";
import type { Action, Meta, Root } from "../../../sdk/ts/src/index.ts";

class Superuser {
  name = field("", { env: "POSTGRES_USER", omitempty: true });
  password = field(new Secret(), { env: "POSTGRES_PASSWORD" }); // required: the operator names a Secret
}

// Conf is postgresql.conf.
class Conf {
  maxConnections = field(0, { json: "max_connections", omitempty: true });
  sharedBuffers = field("", { json: "shared_buffers", omitempty: true }); // a quarter of the memory limit, unless the operator says
  listenAddresses = field("", { json: "listen_addresses", omitempty: true });
  port = field(0, { omitempty: true });
}

export class Postgres extends Pack {
  database = field("", { env: "POSTGRES_DB" }); // required: no omitempty
  superuser = field(new Superuser());
  conf = field(new Conf());
  // where the packages of PGDG come from on a host: upstream by default, a mirror (oci-proxy of the ansible target)
  // when the hosts do not reach it
  pgdg = field("", { omitempty: true });
  pgdgKey = field("", { json: "pgdg_key", omitempty: true });
  // the extensions every database gets
  initExtensions = field([] as string[], { json: "init_extensions", omitempty: true, of: "string" });

  setDefaults(): void {
    this.superuser.name = "postgres";
    this.conf.maxConnections = 100;
    this.conf.listenAddresses = "*";
    this.conf.port = 5432;
    this.pgdg = "https://apt.postgresql.org/pub/repos/apt";
    this.pgdgKey = "https://www.postgresql.org/media/keys/ACCC4CF8.asc";
    this.initExtensions = ["pg_stat_statements", "pgcrypto"];
  }

  meta(): Meta {
    return {
      name: "postgres", version: "16.4", description: "PostgreSQL", license: "PostgreSQL",
      image: "images/postgres:16.4",
    };
  }

  packRoot(): Root {
    return {
      packages: {
        // On a host the same application lives elsewhere, by OS; the version is the OS's, not Meta's
        debian: {
          prepare: ["ca-certificates"],
          repo: [
            { path: "/etc/apt/keyrings/pgdg.asc", url: "{{ .pgdg_key }}", mode: "0644" },
            { path: "/etc/apt/sources.list.d/pgdg.sources", template: "host-pgdg.sources", mode: "0644" },
          ],
          names: ["postgresql-16"],
          content: [
            { path: "/etc/postgresql/16/main/conf.d/pack.conf", template: "host-postgresql.conf", mode: "0644" },
            { path: "/etc/postgresql/16/main/pg_hba.conf", source: "host-pg_hba.conf", mode: "0640" },
            { path: "/opt/pack/init-extensions.sh", template: "host-init.sh", mode: "0700" },
          ],
          services: ["postgresql@16-main"],
        },
        fedora: {
          names: ["postgresql-server", "postgresql-contrib"],
          commands: [["sh", "-c", "test -f /var/lib/pgsql/data/PG_VERSION || postgresql-setup --initdb"]],
          content: [
            { path: "/var/lib/pgsql/data/postgresql.auto.conf", template: "host-postgresql.conf", mode: "0644" },
            { path: "/var/lib/pgsql/data/pg_hba.conf", source: "host-pg_hba.conf", mode: "0644" },
            { path: "/opt/pack/init-extensions.sh", template: "host-init.sh", mode: "0700" },
          ],
          services: ["postgresql"],
        },
      },
      content: [
        // the script of the install hook is rendered: a hook is a container of its own and is told where the server is
        { path: "/etc/postgresql/postgresql.conf", values: "conf", format: "properties" },
        { path: "/opt/pack/init-extensions.sh", template: "init-extensions.sh", mode: "0755" },
        { path: "/opt/pack/restore-15.sh", source: "restore-15.sh", mode: "0755" },
      ],
      ports: { pg: 5432 },
      data: { data: { mountPath: "/var/lib/postgresql/data", size: "10Gi" } },
      minimum: { cpu: "500m", memory: "512Mi" },
      readiness: { tcp: "pg" },
      upgrade: { from: ">=15" },
      hooks: [
        {
          name: "init-extensions", phase: "postInstall", image: "new",
          command: ["/opt/pack/init-extensions.sh"], idempotent: true, timeout: "5m",
        },
        {
          name: "pg-upgrade", phase: "preUpgrade", when: { from: "<16", to: ">=16" }, image: "new",
          command: [
            "/usr/lib/postgresql/16/bin/pg_upgrade", "--old-bindir=/usr/lib/postgresql/15/bin",
            "--new-bindir=/usr/lib/postgresql/16/bin", "--old-datadir=/var/lib/postgresql/data",
            "--new-datadir=/var/lib/postgresql/data.16",
          ],
          exclusive: true, timeout: "30m",
          compensate: { image: "old", command: ["/opt/pack/restore-15.sh"] },
        },
        {
          name: "analyze", phase: "postUpgrade", when: { to: ">=16" }, image: "new",
          command: ["vacuumdb", "--all", "--analyze-in-stages"], idempotent: true,
        },
      ],
    };
  }

  // What an operator runs on the installed postgres besides an apply: a dump, which only reads, and a restore over
  // the database, which the pack never lets run unasked. pg_dump and pg_restore reach the server by the instance's
  // own socket, where the action runs; a dump under the data volume outlives it.
  packActions(): Action[] {
    return [
      {
        name: "backup",
        command: ["pg_dump", "--username={{ .user }}", "--format={{ .format }}", "--file={{ .to }}", "--dbname={{ .database }}"],
        timeout: "1h",
        params: [
          { name: "to", type: "string", required: true },
          { name: "format", type: "string", default: "custom", enum: ["custom", "plain", "directory"] },
          { name: "database", type: "string", default: "postgres" },
          { name: "user", type: "string", default: "postgres" },
        ],
      },
      {
        name: "restore",
        command: ["pg_restore", "--username={{ .user }}", "--clean", "--if-exists", "--dbname={{ .database }}", "{{ .from }}"],
        timeout: "1h", mutating: true, approve: true,
        params: [
          { name: "from", type: "string", required: true },
          { name: "database", type: "string", default: "postgres" },
          { name: "user", type: "string", default: "postgres" },
        ],
      },
    ];
  }

  // A quarter of the memory limit, in MB: the formula fills the leaf when the operator did not write it. The key
  // is the field's pointer, found from the field itself.
  packCompute(): Record<string, string> {
    return {
      [pointer(this, (o) => o.conf.sharedBuffers)]:
        "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'",
    };
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await main(new Postgres());
}
