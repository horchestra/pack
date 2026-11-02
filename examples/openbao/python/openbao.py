"""openbao: a pack that holds another — postgres (examples/postgres/python) is an application of its own, and
optional here. Chosen, it IS the storage: the formula fills openbao.storage from it, and the rule forbids writing
one by hand — the two halves of one fact. The same pack is written in Go and Java next to this one; all three
generate the same artifacts (examples/openbao/golden).

    python3 openbao.py generate -o out
"""

import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "sdk" / "python"))
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "postgres" / "python"))

from packsdk import OSPackages, File, OSPackages, Meta, Pack, Readiness, Root, Rule, Volume, dotted, field, main, pointer
from postgres import Postgres


@dataclass
class TelemetryBlock:
    """Telemetry and metrics settings."""
    disable_hostname: bool = field(False, omitempty=True)
    enable_hostname_label: bool = field(False, omitempty=True)
    metrics_prefix: str = field("", omitempty=True)
    usage_gauge_period: str = field("", omitempty=True)  # a duration ("10s")
    maximum_gauge_cardinality: int = field(0, omitempty=True)
    lease_metrics_threshold: str = field("", omitempty=True)
    filter_default: bool = field(False, omitempty=True)
    prefix_filter: list[str] = field(factory=list, omitempty=True)

    # StatsD / Statsite.
    statsd_address: str = field("", omitempty=True)
    statsite_address: str = field("", omitempty=True)

    # DogStatsD.
    dogstatsd_addr: str = field("", omitempty=True)
    dogstatsd_tags: list[str] = field(factory=list, omitempty=True)

    # Prometheus.
    prometheus_retention_time: str = field("", omitempty=True)


@dataclass
class Config:
    """The root configuration of OpenBao: the subtree becomes /openbao/config/config.json — the JSON form of the
    app's own HCL, so the block sections are objects by their type's name, as the app reads them
    (storage "raft" {} is storage: {raft: {…}}); the pack does not re-model the three dozen backends the app knows."""

    # Identity and addresses.
    cluster_name: str = field("", omitempty=True)
    api_addr: str = field("", omitempty=True)
    cluster_addr: str = field("", omitempty=True)  # raft wants it set

    # Cache and runtime behavior.
    cache_size: int = field(0, omitempty=True)
    disable_cache: bool = field(False, omitempty=True)
    disable_mlock: bool = field(False, omitempty=True)
    disable_clustering: bool = field(False, omitempty=True)

    # UI / API / endpoints.
    ui: bool = field(False, omitempty=True)
    raw_storage_endpoint: bool = field(False, omitempty=True)
    introspection_endpoint: bool = field(False, omitempty=True)
    unsafe_allow_api_in_browser: bool = field(False, omitempty=True)
    disable_printable_check: bool = field(False, omitempty=True)
    administratively_disabled: bool = field(False, omitempty=True)

    # Filesystem paths.
    pid_file: str = field("", omitempty=True)
    plugin_directory: str = field("", omitempty=True)

    # TTLs, as durations ("768h").
    default_lease_ttl: str = field("", omitempty=True)
    max_lease_ttl: str = field("", omitempty=True)

    # Logging. Format is always JSON.
    log_level: str = field("", omitempty=True)  # trace|debug|info|warn|err
    log_file: str = field("", omitempty=True)
    log_rotate_bytes: int = field(0, omitempty=True)
    log_rotate_duration: str = field("", omitempty=True)  # e.g. "24h"
    log_rotate_max_files: int = field(0, omitempty=True)

    # Blocks, in the app's words. With the embedded postgres chosen, storage is the formula's, never the
    # operator's (pack_compute, pack_rules).
    storage: dict[str, Any] = field(factory=dict, omitempty=True)  # {raft: {path, node_id}} — {postgresql: {…}}
    ha_storage: dict[str, Any] = field(factory=dict, omitempty=True)  # the storage of the HA layer, when it differs
    listener: dict[str, Any] = field(factory=dict, omitempty=True)  # {tcp: {address, tls_cert_file, …}}
    seal: dict[str, Any] = field(factory=dict, omitempty=True)  # {static: {current_key_id, current_key}}, {awskms: {…}}
    telemetry: Optional[TelemetryBlock] = field(None, omitempty=True)  # a stanza of its own, typed above
    service_registration: dict[str, Any] = field(factory=dict, omitempty=True)
    initialize: list[dict[str, Any]] = field(factory=list, omitempty=True)  # self-initialization (needs auto-unseal)


@dataclass
class OpenBao(Pack):
    """The pack: the application's own configuration, and the pack it may be installed with."""
    bao: Config = field(factory=Config, json="openbao")
    postgres: Optional[Postgres] = field(None, omitempty=True)
    # Dev runs the server in dev mode — in-memory, unsealed, the config file ignored: the image's own default,
    # for trying the pack out.
    dev: bool = field(False, omitempty=True, env="BAO_DEV")
    # where a package file of a release comes from: GitHub by default, a mirror (oci-packer proxy) when the hosts do not reach it
    release_url: str = field("", omitempty=True)

    def set_defaults(self):
        self.bao.log_level = "info"
        self.bao.disable_mlock = True  # a container cannot mlock
        self.release_url = "https://github.com/openbao/openbao/releases/download"

    def meta(self):
        return Meta(name="openbao", version="2.6.5", description="manage, store and distribute sensitive data",
                    license="MPL-2.0", image="images/openbao:2.6.5")

    def pack_root(self):
        return Root(content=[File(path="/openbao/config/config.json", values="openbao")],
                    # the package of each OS reads the config at its own path and is of its own version
                    packages={
                        "debian": OSPackages(
                            prepare=["ca-certificates", "openssl"],
                            names=["{{ .release_url }}/v2.6.5/openbao_2.6.5_linux_${os:arch}.deb"],
                            content=[File(path="/etc/openbao/openbao.hcl", values="openbao", format="json", mode="0644")],
                            services=["openbao"]),
                        "fedora": OSPackages(
                            names=["openbao"],
                            content=[File(path="/etc/openbao.d/openbao.hcl", values="openbao", format="json", mode="0644")],
                            services=["openbao"]),
                    }, ports={"api": 8200, "cluster": 8201},
                    data={"file": Volume(mount_path="/openbao/file", size="1Gi")},
                    readiness=Readiness(http="api/v1/sys/health"), command=["/bin/sh", "-c", "if [ \"${BAO_DEV:-}\" = true ]; then exec docker-entrypoint.sh server -dev -dev-no-store-token; fi; exec docker-entrypoint.sh server"],
                    savepoints=[dotted(self, lambda o: o.postgres)])

    # The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
    # postgres and wrote no storage himself — the database name and the user are the ones the operator gave
    # postgres, the password the placeholder of the credential pack provisions for it (the target resolves
    # ${secret:…}).
    def pack_compute(self):
        pg = dotted(self, lambda o: o.postgres)
        return {pointer(self, lambda o: o.bao.storage): (
            f"has(pack.values.{pg}) ? {{'postgresql': {{'connection_url': "
            f"'postgres://' + pack.values.{pg}.superuser.name + "
            f"':${{secret:' + pack.installation + '-{pg}-superuser-password/password}}@' + pack.keys.{pg}.instances[0] + "
            f"':' + string(pack.keys.{pg}.ports.pg) + '/' + pack.values.{pg}.database}}}} : null")}

    # ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's, never the
    # operator's.
    def pack_rules(self):
        pg, storage = dotted(self, lambda o: o.postgres), dotted(self, lambda o: o.bao.storage)
        return {pointer(self, lambda o: o.bao.storage): [Rule(
            rule=f"!has(pack.values.{pg}) || !has(values.{storage}) || size(values.{storage}) == 0",
            message="storage is filled from the embedded postgres and must not be set")]}


if __name__ == "__main__":
    sys.exit(main(OpenBao()))
