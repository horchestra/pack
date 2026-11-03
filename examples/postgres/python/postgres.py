"""postgres: a single-instance PostgreSQL with an install hook (the extensions every database gets, after the first
start) and a major-version upgrade hook (the saga runs pg_upgrade with the component stopped, and takes it back if it
fails). The same pack is written in Go and Java next to this one; all three generate the same artifacts
(examples/postgres/golden).

    python3 postgres.py generate -o out
"""

import sys
from dataclasses import dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "sdk" / "python"))

from packsdk import (Action, OSPackages, Param, File, Hook, Meta, Pack, Readiness, Root, Secret, Step, Upgrade, Volume, When, field, main,
                     pointer)


@dataclass
class Superuser:
    name: str = field("", env="POSTGRES_USER", omitempty=True)
    password: Secret = field(factory=Secret, env="POSTGRES_PASSWORD")  # required: the operator names a Secret


@dataclass
class Conf:
    max_connections: int = field(0, omitempty=True)
    shared_buffers: str = field("", omitempty=True)  # a quarter of the memory limit, unless the operator says
    listen_addresses: str = field("", omitempty=True)
    port: int = field(0, omitempty=True)


@dataclass
class Postgres(Pack):
    database: str = field("", env="POSTGRES_DB")  # required: no omitempty
    superuser: Superuser = field(factory=Superuser)
    # postgresql.conf
    conf: Conf = field(factory=Conf)
    # where the packages of PGDG come from on a host: upstream by default, a mirror (oci-proxy of the ansible target)
    # when the hosts do not reach it
    pgdg: str = field("", omitempty=True)
    pgdg_key: str = field("", omitempty=True)
    # the extensions every database gets
    init_extensions: list[str] = field(factory=list, omitempty=True)

    def set_defaults(self):
        self.superuser.name = "postgres"
        self.conf.max_connections = 100
        self.conf.listen_addresses = "*"
        self.conf.port = 5432
        self.pgdg = "https://apt.postgresql.org/pub/repos/apt"
        self.pgdg_key = "https://www.postgresql.org/media/keys/ACCC4CF8.asc"
        self.init_extensions = ["pg_stat_statements", "pgcrypto"]

    def meta(self):
        return Meta(name="postgres", version="16.4", description="PostgreSQL", license="PostgreSQL",
                    image="images/postgres:16.4")

    def pack_root(self):
        return Root(
            packages={
                # On a host the same application lives elsewhere, by OS; the version is the OS's, not Meta's
                "debian": OSPackages(
                    prepare=["ca-certificates"],
                    repo=[File(path="/etc/apt/keyrings/pgdg.asc", url="{{ .pgdg_key }}", mode="0644"),
                          File(path="/etc/apt/sources.list.d/pgdg.sources", template="host-pgdg.sources", mode="0644")],
                    names=["postgresql-16"],
                    content=[File(path="/etc/postgresql/16/main/conf.d/pack.conf", template="host-postgresql.conf", mode="0644"),
                             File(path="/etc/postgresql/16/main/pg_hba.conf", source="host-pg_hba.conf", mode="0640"),
                             File(path="/opt/pack/init-extensions.sh", template="host-init.sh", mode="0700")],
                    services=["postgresql@16-main"]),
                "fedora": OSPackages(
                    names=["postgresql-server", "postgresql-contrib"],
                    commands=[["sh", "-c", "test -f /var/lib/pgsql/data/PG_VERSION || postgresql-setup --initdb"]],
                    content=[File(path="/var/lib/pgsql/data/postgresql.auto.conf", template="host-postgresql.conf", mode="0644"),
                             File(path="/var/lib/pgsql/data/pg_hba.conf", source="host-pg_hba.conf", mode="0644"),
                             File(path="/opt/pack/init-extensions.sh", template="host-init.sh", mode="0700")],
                    services=["postgresql"]),
            },
            content=[
                # the script of the install hook is rendered: a hook is a container of its own and is told where the server is
                File(path="/etc/postgresql/postgresql.conf", values="conf", format="properties"),
                File(path="/opt/pack/init-extensions.sh", template="init-extensions.sh", mode="0755"),
                File(path="/opt/pack/restore-15.sh", source="restore-15.sh", mode="0755"),
            ],
            ports={"pg": 5432},
            data={"data": Volume(mount_path="/var/lib/postgresql/data", size="10Gi")},
            minimum={"cpu": "500m", "memory": "512Mi"},
            readiness=Readiness(tcp="pg"),
            upgrade=Upgrade(from_=">=15"),
            hooks=[
                Hook(name="init-extensions", phase="postInstall", image="new",
                     command=["/opt/pack/init-extensions.sh"], idempotent=True, timeout="5m"),
                Hook(name="pg-upgrade", phase="preUpgrade", when=When(from_="<16", to=">=16"), image="new",
                     command=["/usr/lib/postgresql/16/bin/pg_upgrade", "--old-bindir=/usr/lib/postgresql/15/bin",
                              "--new-bindir=/usr/lib/postgresql/16/bin", "--old-datadir=/var/lib/postgresql/data",
                              "--new-datadir=/var/lib/postgresql/data.16"],
                     exclusive=True, timeout="30m",
                     compensate=Step(image="old", command=["/opt/pack/restore-15.sh"])),
                Hook(name="analyze", phase="postUpgrade", when=When(to=">=16"), image="new",
                     command=["vacuumdb", "--all", "--analyze-in-stages"], idempotent=True),
            ],
        )

    # What an operator runs on the installed postgres besides an apply: a dump, which only reads, and a restore over
    # the database, which the pack never lets run unasked. pg_dump and pg_restore reach the server by the instance's
    # own socket, where the action runs; a dump under the data volume outlives it.
    def pack_actions(self):
        return [
            Action(name="backup", command=["pg_dump", "--username={{ .user }}", "--format={{ .format }}", "--file={{ .to }}", "--dbname={{ .database }}"],
                   timeout="1h",
                   params=[Param(name="to", type="string", required=True),
                           Param(name="format", type="string", default="custom", enum=["custom", "plain", "directory"]),
                           Param(name="database", type="string", default="postgres"),
                           Param(name="user", type="string", default="postgres")]),
            Action(name="restore", command=["pg_restore", "--username={{ .user }}", "--clean", "--if-exists", "--dbname={{ .database }}", "{{ .from }}"],
                   timeout="1h", mutating=True, approve=True,
                   params=[Param(name="from", type="string", required=True),
                           Param(name="database", type="string", default="postgres"),
                           Param(name="user", type="string", default="postgres")]),
        ]

    # A quarter of the memory limit, in MB: the formula fills the leaf when the operator did not write it. The key
    # is the field's pointer, found from the field itself.
    def pack_compute(self):
        return {pointer(self, lambda o: o.conf.shared_buffers):
                "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'"}


if __name__ == "__main__":
    sys.exit(main(Postgres()))
