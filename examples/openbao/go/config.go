package openbao

// Config is the root configuration of OpenBao: the subtree becomes
// /openbao/config/config.json — the JSON form of the app's own HCL, so the
// block sections are objects by their type's name, as the app reads them
// (storage "raft" {} is storage: {raft: {…}}); the pack does not re-model the
// three dozen backends the app knows.
type Config struct {
	// Identity and addresses.
	ClusterName string `json:"cluster_name,omitempty"`
	APIAddr     string `json:"api_addr,omitempty"`
	ClusterAddr string `json:"cluster_addr,omitempty"` // raft wants it set

	// Cache and runtime behavior.
	CacheSize         int  `json:"cache_size,omitempty"`
	DisableCache      bool `json:"disable_cache,omitempty"`
	DisableMlock      bool `json:"disable_mlock,omitempty"`
	DisableClustering bool `json:"disable_clustering,omitempty"`

	// UI / API / endpoints.
	UI                       bool `json:"ui,omitempty"`
	RawStorageEndpoint       bool `json:"raw_storage_endpoint,omitempty"`
	IntrospectionEndpoint    bool `json:"introspection_endpoint,omitempty"`
	UnsafeAllowAPIBrowser    bool `json:"unsafe_allow_api_in_browser,omitempty"`
	DisablePrintableCheck    bool `json:"disable_printable_check,omitempty"`
	AdministrativelyDisabled bool `json:"administratively_disabled,omitempty"`

	// Filesystem paths.
	PIDFile         string `json:"pid_file,omitempty"`
	PluginDirectory string `json:"plugin_directory,omitempty"`

	// TTLs, as durations ("768h").
	DefaultLeaseTTL string `json:"default_lease_ttl,omitempty"`
	MaxLeaseTTL     string `json:"max_lease_ttl,omitempty"`

	// Logging. Format is always JSON.
	LogLevel          string `json:"log_level,omitempty"` // trace|debug|info|warn|err
	LogFile           string `json:"log_file,omitempty"`
	LogRotateBytes    int    `json:"log_rotate_bytes,omitempty"`
	LogRotateDuration string `json:"log_rotate_duration,omitempty"` // e.g. "24h"
	LogRotateMaxFiles int    `json:"log_rotate_max_files,omitempty"`

	// Blocks, in the app's words. With the embedded postgres chosen, storage
	// is the formula's, never the operator's (PackCompute, PackRules).
	Storage             map[string]any   `json:"storage,omitempty"`    // {raft: {path, node_id}} — the formula fills {postgresql: {…}}
	HAStorage           map[string]any   `json:"ha_storage,omitempty"` // the storage of the HA layer, when it differs
	Listener            map[string]any   `json:"listener,omitempty"`   // {tcp: {address, tls_cert_file, tls_key_file, …}}
	Seal                map[string]any   `json:"seal,omitempty"`       // {static: {current_key_id, current_key}}, {awskms: {…}}
	Telemetry           *TelemetryBlock  `json:"telemetry,omitempty"`  // a stanza of its own, typed below
	ServiceRegistration map[string]any   `json:"service_registration,omitempty"`
	Initialize          []map[string]any `json:"initialize,omitempty"` // self-initialization on the first start (needs an auto-unseal seal)
}

// TelemetryBlock holds telemetry and metrics settings.
type TelemetryBlock struct {
	DisableHostname         bool     `json:"disable_hostname,omitempty"`
	EnableHostnameLabel     bool     `json:"enable_hostname_label,omitempty"`
	MetricsPrefix           string   `json:"metrics_prefix,omitempty"`
	UsageGaugePeriod        string   `json:"usage_gauge_period,omitempty"` // a duration ("10s")
	MaximumGaugeCardinality int      `json:"maximum_gauge_cardinality,omitempty"`
	LeaseMetricsThreshold   string   `json:"lease_metrics_threshold,omitempty"`
	FilterDefault           bool     `json:"filter_default,omitempty"`
	PrefixFilter            []string `json:"prefix_filter,omitempty"`

	// StatsD / Statsite.
	StatsdAddress   string `json:"statsd_address,omitempty"`
	StatsiteAddress string `json:"statsite_address,omitempty"`

	// DogStatsD.
	DogstatsdAddr string   `json:"dogstatsd_addr,omitempty"`
	DogstatsdTags []string `json:"dogstatsd_tags,omitempty"`

	// Prometheus.
	PrometheusRetentionTime string `json:"prometheus_retention_time,omitempty"`
}
