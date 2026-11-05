// docker: a pack that makes a host a Docker host: the engine from Docker's own repositories
// (https://docs.docker.com/engine/install/), its daemon.json and its unit. It has no image: it is for a target that
// installs OS packages. The host is taken to be empty, nothing conflicting is looked for or removed. The same pack
// is written in Go, Python and Java next to this one; all four generate the same artifacts (examples/docker/golden).
//
//     node docker.ts generate -o out

import { pathToFileURL } from "node:url";

import { field, main, Pack } from "../../../sdk/ts/src/index.ts";
import type { Meta, PackFile, Root } from "../../../sdk/ts/src/index.ts";

class LogOpts {
  maxSize = field("", { json: "max-size", omitempty: true });
  maxFile = field("", { json: "max-file", omitempty: true });
}

// Daemon is /etc/docker/daemon.json.
class Daemon {
  logDriver = field("", { json: "log-driver", omitempty: true });
  logOpts = field(new LogOpts(), { json: "log-opts" });
}

// Docker is the pack. mirror is where the packages come from: Docker's own by default, a mirror of the operator's
// (oci-packer proxy) when the hosts do not reach it.
export class Docker extends Pack {
  mirror = field("", { omitempty: true });
  daemon = field(new Daemon());

  setDefaults(): void {
    this.mirror = "https://download.docker.com/linux";
    this.daemon.logDriver = "json-file";
    this.daemon.logOpts.maxSize = "50m";
    this.daemon.logOpts.maxFile = "3";
  }

  meta(): Meta {
    return { name: "docker", version: "1.0.0", description: "Docker Engine from OS packages", license: "Apache-2.0" };
  }

  packRoot(): Root {
    const engine = ["docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"];
    const daemon: PackFile = { path: "/etc/docker/daemon.json", values: "daemon", format: "json", mode: "0644" };
    return {
      packages: {
        debian: {
          prepare: ["ca-certificates"],
          repo: [
            { path: "/etc/apt/keyrings/docker.asc", url: "{{ .mirror }}/debian/gpg", mode: "0644" },
            { path: "/etc/apt/sources.list.d/docker.sources", template: "docker.sources", mode: "0644" },
          ],
          names: engine,
          content: [daemon],
          services: ["docker"],
        },
        fedora: {
          repo: [{ path: "/etc/yum.repos.d/docker-ce.repo", template: "docker-ce.repo", mode: "0644" }],
          names: engine,
          content: [daemon],
          services: ["docker"],
        },
      },
    };
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await main(new Docker());
}
