"""tembo, the pack of the Go tests (sdk/internal/testpacks/tembo), written in Python."""

from dataclasses import dataclass

from packsdk import Action, Diagnosis, Param, File, Fix, Meta, Pack, Readiness, Root, Secret, Suggest, Volume, field, pointer


@dataclass
class Node:
    """The node's tags and the log it keeps."""
    raft_retention_log_size: int = field(0, omitempty=True)
    nofailover: bool = field(False, omitempty=True)
    nosync: bool = field(False, omitempty=True)
    failover_priority: int = field(0, omitempty=True)


@dataclass
class Tembo(Pack):
    id: str = field("", env="TEMBO_ID", omitempty=True, pattern="^[a-z0-9_]*$", max_length=63)
    listen: str = field("", env="TEMBO_LISTEN", omitempty=True)
    listen_ro: str = field("", env="TEMBO_LISTEN_RO", omitempty=True)
    db: str = field("", env="TEMBO_DB", omitempty=True)
    pg_data: str = field("", env="TEMBO_PG_DATA", omitempty=True)
    pg_port: int = field(0, env="TEMBO_PG_PORT", omitempty=True, minimum=1, maximum=65535)
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

    # the port next to the agent's own when a host of the target already listens on it
    def pack_suggest(self):
        return {pointer(self, lambda o: o.pg_port): Suggest(
            expr="has(facts.hosts) && facts.hosts.exists(h, has(facts.hosts[h].ports) && facts.hosts[h].ports.exists(p, p.port == 5433)) ? 5434 : null",
            why="5433 is taken on a host")}

    # postgres that cannot listen because something on the host already does
    def pack_actions(self):
        return [Action(name="backup", command=["tembo", "backup", "--to", "{{ .to }}", "--format", "{{ .format }}"], timeout="30m",
                       params=[Param(name="to", type="string", required=True),
                               Param(name="format", type="string", default="custom", enum=["custom", "plain"])]),
                Action(name="restore", command=["tembo", "restore", "--from", "{{ .from }}"], mutating=True, approve=True,
                       params=[Param(name="from", type="string", required=True)])]

    def pack_diagnose(self):
        return [Diagnosis(id="port-taken", match="address already in use.*:5433|could not bind .*5433",
                          cause="postgres cannot listen: something on the host already listens on 5433",
                          fix=[Fix(patch=pointer(self, lambda o: o.pg_port), expr="5434", why="the port next to it"),
                               Fix(command="pack discover", why="see what listens on the hosts")])]

    def pack_root(self):
        return Root(
            ports={"api": 8001, "pg": 5432, "pg-ro": 5434},
            content=[File(path="/etc/tembo/tembo.env", template="tembo.env.ptmpl", values="node", parse_as="text", mode="0640")],
            data={"data": Volume(mount_path="/var/lib/postgresql", size="10Gi")},
            minimum={"memory": "1Gi"},
            command=["tembo", "run"],
            readiness=Readiness(http="api/health"),
        )
