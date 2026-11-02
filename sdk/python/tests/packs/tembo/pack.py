"""tembo, the pack of the Go tests (internal/testpacks/tembo), written in Python."""

from dataclasses import dataclass

from packsdk import File, Meta, Pack, Readiness, Root, Secret, Volume, field


@dataclass
class Node:
    """The node's tags and the log it keeps."""
    raft_retention_log_size: int = field(0, omitempty=True)
    nofailover: bool = field(False, omitempty=True)
    nosync: bool = field(False, omitempty=True)
    failover_priority: int = field(0, omitempty=True)


@dataclass
class Tembo(Pack):
    id: str = field("", env="TEMBO_ID", omitempty=True)
    listen: str = field("", env="TEMBO_LISTEN", omitempty=True)
    listen_ro: str = field("", env="TEMBO_LISTEN_RO", omitempty=True)
    db: str = field("", env="TEMBO_DB", omitempty=True)
    pg_data: str = field("", env="TEMBO_PG_DATA", omitempty=True)
    pg_port: int = field(0, env="TEMBO_PG_PORT", omitempty=True)
    pg_password: Secret = field(factory=Secret, env="TEMBO_PG_PASSWORD", omitempty=True)
    config: str = field("", env="TEMBO_CONFIG", omitempty=True)
    node: Node = field(factory=Node, omitempty=True)

    def set_defaults(self):
        self.listen = ":5432"
        self.listen_ro = ":5434"
        self.db = "/var/lib/postgresql/tembo.db"
        self.pg_data = "/var/lib/postgresql/data"
        self.pg_port = 5433
        self.config = "/etc/tembo/tembo.env"
        self.node.raft_retention_log_size = 8 << 20

    def meta(self):
        return Meta(name="tembo", version="0.2.0", description="PostgreSQL with raft", license="Apache-2.0",
                    source="https://example.org/tembo", image="images/tembo:0.2.0")

    def pack_root(self):
        return Root(
            ports={"api": 8001, "pg": 5432, "pg-ro": 5434},
            content=[File(path="/etc/tembo/tembo.env", template="tembo.env.ptmpl", values="node", parse_as="text", mode="0640")],
            data={"data": Volume(mount_path="/var/lib/postgresql", size="10Gi")},
            minimum={"memory": "1Gi"},
            command=["tembo", "run"],
            readiness=Readiness(http="api/health"),
        )
