import io.horchestra.pack.sdk.Meta;
import io.horchestra.pack.sdk.Pack;
import io.horchestra.pack.sdk.PackSdk;
import io.horchestra.pack.sdk.Paths;
import io.horchestra.pack.sdk.Root;
import io.horchestra.pack.sdk.Rule;
import io.horchestra.pack.sdk.Setting;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * openbao: a pack that holds another — postgres (examples/postgres/java) is an application of its own, and
 * optional here. Chosen, it IS the storage: the formula fills openbao.storage from it, and the rule forbids
 * writing one by hand — the two halves of one fact. The same pack is written in Go and Python next to this one;
 * all three generate the same artifacts (examples/openbao/golden).
 *
 * <pre>
 * java -cp out OpenBao generate -o out/pack     (from examples/openbao/java; see ../../../sdk/java/README.md)
 * </pre>
 */
public class OpenBao extends Pack {
    /** Telemetry and metrics settings. */
    public static class TelemetryBlock {
        @Setting(json = "disable_hostname", omitempty = true)
        public boolean disableHostname;
        @Setting(json = "enable_hostname_label", omitempty = true)
        public boolean enableHostnameLabel;
        @Setting(json = "metrics_prefix", omitempty = true)
        public String metricsPrefix = "";
        @Setting(json = "usage_gauge_period", omitempty = true) // a duration ("10s")
        public String usageGaugePeriod = "";
        @Setting(json = "maximum_gauge_cardinality", omitempty = true)
        public int maximumGaugeCardinality;
        @Setting(json = "lease_metrics_threshold", omitempty = true)
        public String leaseMetricsThreshold = "";
        @Setting(json = "filter_default", omitempty = true)
        public boolean filterDefault;
        @Setting(json = "prefix_filter", omitempty = true)
        public List<String> prefixFilter = new ArrayList<>();

        // StatsD / Statsite.
        @Setting(json = "statsd_address", omitempty = true)
        public String statsdAddress = "";
        @Setting(json = "statsite_address", omitempty = true)
        public String statsiteAddress = "";

        // DogStatsD.
        @Setting(json = "dogstatsd_addr", omitempty = true)
        public String dogstatsdAddr = "";
        @Setting(json = "dogstatsd_tags", omitempty = true)
        public List<String> dogstatsdTags = new ArrayList<>();

        // Prometheus.
        @Setting(json = "prometheus_retention_time", omitempty = true)
        public String prometheusRetentionTime = "";
    }

    /** The root configuration of OpenBao: the subtree becomes /openbao/config/config.json — the JSON form of the
     *  app's own HCL, so the block sections are objects by their type's name, as the app reads them
     *  (storage "raft" {} is storage: {raft: {…}}); the pack does not re-model the three dozen backends the app
     *  knows. */
    public static class Config {
        // Identity and addresses.
        @Setting(json = "cluster_name", omitempty = true)
        public String clusterName = "";
        @Setting(json = "api_addr", omitempty = true)
        public String apiAddr = "";
        @Setting(json = "cluster_addr", omitempty = true) // raft wants it set
        public String clusterAddr = "";

        // Cache and runtime behavior.
        @Setting(json = "cache_size", omitempty = true)
        public int cacheSize;
        @Setting(json = "disable_cache", omitempty = true)
        public boolean disableCache;
        @Setting(json = "disable_mlock", omitempty = true)
        public boolean disableMlock;
        @Setting(json = "disable_clustering", omitempty = true)
        public boolean disableClustering;

        // UI / API / endpoints.
        @Setting(omitempty = true)
        public boolean ui;
        @Setting(json = "raw_storage_endpoint", omitempty = true)
        public boolean rawStorageEndpoint;
        @Setting(json = "introspection_endpoint", omitempty = true)
        public boolean introspectionEndpoint;
        @Setting(json = "unsafe_allow_api_in_browser", omitempty = true)
        public boolean unsafeAllowApiBrowser;
        @Setting(json = "disable_printable_check", omitempty = true)
        public boolean disablePrintableCheck;
        @Setting(json = "administratively_disabled", omitempty = true)
        public boolean administrativelyDisabled;

        // Filesystem paths.
        @Setting(json = "pid_file", omitempty = true)
        public String pidFile = "";
        @Setting(json = "plugin_directory", omitempty = true)
        public String pluginDirectory = "";

        // TTLs, as durations ("768h").
        @Setting(json = "default_lease_ttl", omitempty = true)
        public String defaultLeaseTtl = "";
        @Setting(json = "max_lease_ttl", omitempty = true)
        public String maxLeaseTtl = "";

        // Logging. Format is always JSON.
        @Setting(json = "log_level", omitempty = true) // trace|debug|info|warn|err
        public String logLevel = "";
        @Setting(json = "log_file", omitempty = true)
        public String logFile = "";
        @Setting(json = "log_rotate_bytes", omitempty = true)
        public int logRotateBytes;
        @Setting(json = "log_rotate_duration", omitempty = true) // e.g. "24h"
        public String logRotateDuration = "";
        @Setting(json = "log_rotate_max_files", omitempty = true)
        public int logRotateMaxFiles;

        // Blocks, in the app's words. With the embedded postgres chosen, storage is the formula's, never the
        // operator's (packCompute, packRules).
        @Setting(omitempty = true)
        public Map<String, Object> storage = new TreeMap<>(); // {raft: {path, node_id}} — {postgresql: {…}}
        @Setting(json = "ha_storage", omitempty = true)
        public Map<String, Object> haStorage = new TreeMap<>(); // the storage of the HA layer, when it differs
        @Setting(omitempty = true)
        public Map<String, Object> listener = new TreeMap<>(); // {tcp: {address, tls_cert_file, …}}
        @Setting(omitempty = true)
        public Map<String, Object> seal = new TreeMap<>(); // {static: {current_key_id, current_key}}, {awskms: {…}}
        @Setting(omitempty = true)
        public TelemetryBlock telemetry; // a stanza of its own, typed above
        @Setting(json = "service_registration", omitempty = true)
        public Map<String, Object> serviceRegistration = new TreeMap<>();
        @Setting(omitempty = true)
        public List<Map<String, Object>> initialize = new ArrayList<>(); // self-initialization (needs auto-unseal)
    }

    /** The pack: the application's own configuration, and the pack it may be installed with. */
    @Setting(json = "openbao")
    public Config bao = new Config();
    @Setting(omitempty = true)
    public Postgres postgres;
    /** Dev runs the server in dev mode — in-memory, unsealed, the config file ignored: the image's own default,
     *  for trying the pack out. */
    @Setting(omitempty = true, env = "BAO_DEV")
    public boolean dev;
    /** Where a package file of a release comes from: GitHub by default, a mirror (oci-packer proxy) when the hosts do not reach it. */
    @Setting(json = "release_url", omitempty = true)
    public String releaseUrl = "";

    @Override
    public void setDefaults() {
        bao.logLevel = "info";
        bao.disableMlock = true; // a container cannot mlock
        releaseUrl = "https://github.com/openbao/openbao/releases/download";
    }

    @Override
    public Meta meta() {
        return new Meta("openbao", "2.6.5").description("manage, store and distribute sensitive data")
                .license("MPL-2.0").image("images/openbao:2.6.5");
    }

    @Override
    public Root packRoot() {
        return new Root()
                .content(new Root.File("/openbao/config/config.json").values("openbao"))
                // the package of each OS reads the config at its own path and is of its own version
                .packages("debian", new Root.OSInstall()
                        .prepare("ca-certificates", "openssl")
                        .names("{{ .release_url }}/v2.6.5/openbao_2.6.5_linux_${os:arch}.deb")
                        .content(new Root.File("/etc/openbao/openbao.hcl").values("openbao").format("json").mode("0644"))
                        .services("openbao"))
                .packages("fedora", new Root.OSInstall()
                        .names("openbao")
                        .content(new Root.File("/etc/openbao.d/openbao.hcl").values("openbao").format("json").mode("0644"))
                        .services("openbao"))
                .port("api", 8200).port("cluster", 8201)
                .data("file", new Root.Volume("/openbao/file").size("1Gi"))
                .readiness(Root.Readiness.http("api/v1/sys/health"))
                .command("/bin/sh", "-c", "if [ \"${BAO_DEV:-}\" = true ]; then exec docker-entrypoint.sh server -dev -dev-no-store-token; fi; exec docker-entrypoint.sh server")
                .savepoint(Paths.dotted(OpenBao.class, "postgres"));
    }

    // The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
    // postgres and wrote no storage himself — the database name and the user are the ones the operator gave
    // postgres, the password the placeholder of the credential pack provisions for it (the target resolves
    // ${secret:…}).
    @Override
    public Map<String, String> packCompute() {
        String pg = Paths.dotted(OpenBao.class, "postgres");
        return Map.of(Paths.pointer(OpenBao.class, "bao", "storage"),
                "has(pack.values." + pg + ") ? {'postgresql': {'connection_url': 'postgres://' + pack.values." + pg
                        + ".superuser.name + ':${secret:' + pack.installation + '-" + pg + "-superuser-password/password}@' + pack.keys."
                        + pg + ".instances[0] + ':' + string(pack.keys." + pg + ".ports.pg) + '/' + pack.values." + pg + ".database}} : null");
    }

    // ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's, never the
    // operator's.
    @Override
    public Map<String, List<Rule>> packRules() {
        String pg = Paths.dotted(OpenBao.class, "postgres"), storage = Paths.dotted(OpenBao.class, "bao", "storage");
        return Map.of(Paths.pointer(OpenBao.class, "bao", "storage"), List.of(
                new Rule("!has(pack.values." + pg + ") || !has(values." + storage + ") || size(values." + storage + ") == 0")
                        .message("storage is filled from the embedded postgres and must not be set").id("storage-from-postgres")));
    }

    public static void main(String[] args) {
        System.exit(PackSdk.main(args, new OpenBao()));
    }
}
