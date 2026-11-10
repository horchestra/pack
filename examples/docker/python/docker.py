"""docker: a pack that makes a host a Docker host: the engine from Docker's own repositories
(https://docs.docker.com/engine/install/), its daemon.json and its unit. It has no image: it is for a target that
installs OS packages. The host is taken to be empty, nothing conflicting is looked for or removed. The same pack is
written in Go and Java next to this one; all three generate the same artifacts (examples/docker/golden).

    python3 docker.py generate -o out
"""

import sys
from dataclasses import dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "sdk" / "python"))

from packsdk import File, Meta, OSInstall, Pack, Root, field, main


@dataclass
class LogOpts:
    max_size: str = field("", json="max-size", omitempty=True)
    max_file: str = field("", json="max-file", omitempty=True)


@dataclass
class Daemon:
    log_driver: str = field("", json="log-driver", omitempty=True)
    log_opts: LogOpts = field(factory=LogOpts, json="log-opts")


@dataclass
class Docker(Pack):
    # where the packages come from: Docker's own by default, a mirror of the operator's (oci-packer proxy) when the
    # hosts do not reach it
    mirror: str = field("", omitempty=True)
    daemon: Daemon = field(factory=Daemon)

    def set_defaults(self):
        self.mirror = "https://download.docker.com/linux"
        self.daemon.log_driver = "json-file"
        self.daemon.log_opts = LogOpts(max_size="50m", max_file="3")

    def meta(self):
        return Meta(name="docker", version="1.0.0", description="Docker Engine from OS packages", license="Apache-2.0",
                    dir=str(Path(__file__).resolve().parent))

    def pack_root(self):
        engine = ["docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"]
        daemon = File(path="/etc/docker/daemon.json", values="daemon", format="json", mode="0644")
        return Root(packages={
            "debian": OSInstall(
                prepare=["ca-certificates"],
                repo=[File(path="/etc/apt/keyrings/docker.asc", url="{{ .mirror }}/debian/gpg", mode="0644"),
                      File(path="/etc/apt/sources.list.d/docker.sources", template="docker.sources", mode="0644")],
                names=engine, content=[daemon], services=["docker"]),
            "fedora": OSInstall(
                repo=[File(path="/etc/yum.repos.d/docker-ce.repo", template="docker-ce.repo", mode="0644")],
                names=engine, content=[daemon], services=["docker"]),
        })


if __name__ == "__main__":
    sys.exit(main(Docker()))
