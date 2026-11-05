// openbao: a pack that holds another — postgres (examples/postgres/ts) is an application of its own, and optional
// here. Chosen, it IS the storage: the formula fills openbao.storage from it, and the rule forbids writing one by
// hand — the two halves of one fact. The same pack is written in Go, Python and Java next to this one; all four
// generate the same artifacts (examples/openbao/golden).
//
//     node openbao.ts generate -o out

import { pathToFileURL } from "node:url";

import { dotted, field, main, Pack, pointer } from "../../../sdk/ts/src/index.ts";
import type { Meta, Root, Rule } from "../../../sdk/ts/src/index.ts";

import { Postgres } from "../../postgres/ts/postgres.ts";

// TelemetryBlock holds telemetry and metrics settings.
class TelemetryBlock {
  disableHostname = field(false, { json: "disable_hostname", omitempty: true });
  enableHostnameLabel = field(false, { json: "enable_hostname_label", omitempty: true });
  metricsPrefix = field("", { json: "metrics_prefix", omitempty: true });
  usageGaugePeriod = field("", { json: "usage_gauge_period", omitempty: true }); // a duration ("10s")
  maximumGaugeCardinality = field(0, { json: "maximum_gauge_cardinality", omitempty: true });
  leaseMetricsThreshold = field("", { json: "lease_metrics_threshold", omitempty: true });
  filterDefault = field(false, { json: "filter_default", omitempty: true });
  prefixFilter = field([] as string[], { json: "prefix_filter", omitempty: true, of: "string" });

  // StatsD / Statsite.
  statsdAddress = field("", { json: "statsd_address", omitempty: true });
  statsiteAddress = field("", { json: "statsite_address", omitempty: true });

  // DogStatsD.
  dogstatsdAddr = field("", { json: "dogstatsd_addr", omitempty: true });
  dogstatsdTags = field([] as string[], { json: "dogstatsd_tags", omitempty: true, of: "string" });

  // Prometheus.
  prometheusRetentionTime = field("", { json: "prometheus_retention_time", omitempty: true });
}

// Config is the root configuration of OpenBao: the subtree becomes /openbao/config/config.json — the JSON form of
// the app's own HCL, so the block sections are objects by their type's name, as the app reads them
// (storage "raft" {} is storage: {raft: {…}}); the pack does not re-model the three dozen backends the app knows.
class Config {
  // Identity and addresses.
  clusterName = field("", { json: "cluster_name", omitempty: true });
  apiAddr = field("", { json: "api_addr", omitempty: true });
  clusterAddr = field("", { json: "cluster_addr", omitempty: true }); // raft wants it set

  // Cache and runtime behavior.
  cacheSize = field(0, { json: "cache_size", omitempty: true });
  disableCache = field(false, { json: "disable_cache", omitempty: true });
  disableMlock = field(false, { json: "disable_mlock", omitempty: true });
  disableClustering = field(false, { json: "disable_clustering", omitempty: true });

  // UI / API / endpoints.
  ui = field(false, { omitempty: true });
  rawStorageEndpoint = field(false, { json: "raw_storage_endpoint", omitempty: true });
  introspectionEndpoint = field(false, { json: "introspection_endpoint", omitempty: true });
  unsafeAllowApiInBrowser = field(false, { json: "unsafe_allow_api_in_browser", omitempty: true });
  disablePrintableCheck = field(false, { json: "disable_printable_check", omitempty: true });
  administrativelyDisabled = field(false, { json: "administratively_disabled", omitempty: true });

  // Filesystem paths.
  pidFile = field("", { json: "pid_file", omitempty: true });
  pluginDirectory = field("", { json: "plugin_directory", omitempty: true });

  // TTLs, as durations ("768h").
  defaultLeaseTtl = field("", { json: "default_lease_ttl", omitempty: true });
  maxLeaseTtl = field("", { json: "max_lease_ttl", omitempty: true });

  // Logging. Format is always JSON.
  logLevel = field("", { json: "log_level", omitempty: true }); // trace|debug|info|warn|err
  logFile = field("", { json: "log_file", omitempty: true });
  logRotateBytes = field(0, { json: "log_rotate_bytes", omitempty: true });
  logRotateDuration = field("", { json: "log_rotate_duration", omitempty: true }); // e.g. "24h"
  logRotateMaxFiles = field(0, { json: "log_rotate_max_files", omitempty: true });

  // Blocks, in the app's words. With the embedded postgres chosen, storage is the formula's, never the
  // operator's (packCompute, packRules).
  storage = field({} as Record<string, unknown>, { omitempty: true }); // {raft: {path, node_id}} — the formula fills {postgresql: {…}}
  haStorage = field({} as Record<string, unknown>, { json: "ha_storage", omitempty: true }); // the storage of the HA layer, when it differs
  listener = field({} as Record<string, unknown>, { omitempty: true }); // {tcp: {address, tls_cert_file, tls_key_file, …}}
  seal = field({} as Record<string, unknown>, { omitempty: true }); // {static: {current_key_id, current_key}}, {awskms: {…}}
  telemetry = field(new TelemetryBlock(), { omitempty: true }); // a stanza of its own, typed above
  serviceRegistration = field({} as Record<string, unknown>, { json: "service_registration", omitempty: true });
  initialize = field([] as Record<string, unknown>[], { omitempty: true, of: "object" }); // self-initialization on the first start (needs an auto-unseal seal)
}

// OpenBao is the pack: the application's own configuration, and the pack it may be installed with.
export class OpenBao extends Pack {
  bao = field(new Config(), { json: "openbao" });
  postgres = field(null as Postgres | null, { pack: Postgres, omitempty: true });
  // dev runs the server in dev mode — in-memory, unsealed, the config file ignored: the image's own default,
  // for trying the pack out. The command of the pack reads it; anything else runs the rendered config.
  dev = field(false, { omitempty: true, env: "BAO_DEV" });
  // releaseUrl is where a package file of a release comes from: GitHub by default, a mirror (oci-packer proxy)
  // when the hosts do not reach it.
  releaseUrl = field("", { json: "release_url", omitempty: true });

  setDefaults(): void {
    this.bao.logLevel = "info";
    this.bao.disableMlock = true; // a container cannot mlock
    this.releaseUrl = "https://github.com/openbao/openbao/releases/download";
  }

  meta(): Meta {
    return {
      name: "openbao", version: "2.6.5", description: "manage, store and distribute sensitive data",
      license: "MPL-2.0", image: "images/openbao:2.6.5",
    };
  }

  packRoot(): Root {
    return {
      ports: { api: 8200, cluster: 8201 },
      content: [{ path: "/openbao/config/config.json", values: "openbao" }],
      // The package of each OS reads the config at its own path and is of its own version: Fedora's own, the
      // release's file on Debian, which has no repository; releaseUrl says where that comes from.
      packages: {
        debian: {
          prepare: ["ca-certificates", "openssl"],
          names: ["{{ .release_url }}/v2.6.5/openbao_2.6.5_linux_${os:arch}.deb"],
          content: [{ path: "/etc/openbao/openbao.hcl", values: "openbao", format: "json", mode: "0644" }],
          services: ["openbao"],
        },
        fedora: {
          names: ["openbao"],
          content: [{ path: "/etc/openbao.d/openbao.hcl", values: "openbao", format: "json", mode: "0644" }],
          services: ["openbao"],
        },
      },
      data: { file: { mountPath: "/openbao/file", size: "1Gi" } },
      readiness: { http: "api/v1/sys/health" },
      command: ["/bin/sh", "-c", 'if [ "${BAO_DEV:-}" = true ]; then exec docker-entrypoint.sh server -dev -dev-no-store-token; fi; exec docker-entrypoint.sh server'],
      savepoints: [dotted(this, (o) => o.postgres)],
    };
  }

  // The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
  // postgres and wrote no storage himself — the database name and the user are the ones the operator gave postgres,
  // the password the placeholder of the credential pack provisions for it (the target resolves ${secret:…}).
  packCompute(): Record<string, string> {
    const pg = dotted(this, (o) => o.postgres);
    return {
      [pointer(this, (o) => o.bao.storage)]:
        `has(pack.values.${pg}) ? {'postgresql': {'connection_url': ` +
        `'postgres://' + pack.values.${pg}.superuser.name + ` +
        `':\${secret:' + pack.installation + '-${pg}-superuser-password/password}@' + pack.keys.${pg}.instances[0] + ':' + ` +
        `string(pack.keys.${pg}.ports.pg) + '/' + pack.values.${pg}.database}} : null`,
    };
  }

  // ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's, never the
  // operator's.
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
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await main(new OpenBao());
}
