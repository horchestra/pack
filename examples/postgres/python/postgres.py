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

from packsdk import (Hook, Meta, Pack, Readiness, Root, Secret, Step, Upgrade, Volume, When, field, main,
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
    database: str = field("")  # required: no omitempty
    superuser: Superuser = field(factory=Superuser)
    # postgresql.conf, written from the subtree as properties
    conf: Conf = field(factory=Conf, file="/etc/postgresql/postgresql.conf", format="properties")
    # the scripts the hooks run are files of content/, shipped as they are
    init_extensions: str = field("", omitempty=True, file="/opt/pack/init-extensions.sh",
                                 template="init-extensions.sh", format="raw", mode="0755")
    restore_15: str = field("", omitempty=True, file="/opt/pack/restore-15.sh",
                            template="restore-15.sh", format="raw", mode="0755")

    def set_defaults(self):
        self.superuser.name = "postgres"
        self.conf.max_connections = 100
        self.conf.listen_addresses = "*"
        self.conf.port = 5432

    def meta(self):
        return Meta(name="postgres", version="16.4", description="PostgreSQL", license="PostgreSQL",
                    image="oci://images/postgres:16.4", artifact="oci://packs/postgres")

    def pack_root(self):
        return Root(
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

    # A quarter of the memory limit, in MB: the formula fills the leaf when the operator did not write it. The key
    # is the field's pointer, found from the field itself.
    def pack_compute(self):
        return {pointer(self, lambda o: o.conf.shared_buffers):
                "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'"}


if __name__ == "__main__":
    sys.exit(main(Postgres()))
