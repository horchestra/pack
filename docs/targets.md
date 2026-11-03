# Targets

A **target** is the system a pack is installed on. The core knows none by name: it renders a pack for one target (names,
addresses and instances of the keys end up inside config files), plans the saga and hands the target one step at a time.
A target says what the environment's placement of a key means on its system and performs the steps in it.

The targets are in their own
repository, [github.com/horchestra/targets](https://github.com/horchestra/targets/tree/main): three modules, each with
its own `go.mod`
and `main.go`, built into a program that pack starts and speaks to over gRPC:

| `target:` | program               |                                                                                                                                       |
|-----------|-----------------------|---------------------------------------------------------------------------------------------------------------------------------------|
| `docker`  | `pack-target-docker`  | [Docker engines](https://github.com/horchestra/targets/tree/main/docker#readme): one, or several hosts, over the CLI or an SSH tunnel |
| `ansible` | `pack-target-ansible` | [classic hosts](https://github.com/horchestra/targets/tree/main/ansible#readme): packages, files and systemd services over SSH        |
| `k8s`     | `pack-target-k8s`     | [Kubernetes](https://github.com/horchestra/targets/tree/main/k8s#readme): StatefulSets, Jobs, Services, Secrets, with client-go       |
| `memory`  | (in pack)             | runs nothing; keeps what it was told in the process — for plans, templates and tests                                                  |

## Using one

Like terraform's providers, a target is named in the release and **downloaded by `pack refresh`** (and by `pack init`, which makes the release):

```yaml
# env.yaml
target: docker
```

```yaml
# release.yaml
targets:
  docker: https://github.com/horchestra/targets/releases/download/docker/v0.1.0/pack-target-docker_{os}_{arch}.tar.gz
  k8s: oci://ghcr.io/horchestra/targets/k8s:0.1.0-{os}-{arch}
```

`pack refresh` takes the targets the environment uses (`target:` and the `target:` of each entry of `targets:`), downloads
each from its source into `.pack/bin/pack-target-<name>`, and writes the SHA-256 of what came into `release.lock.json`:

```json
{
  "targets": {
    "docker": {
      "source": "https://…/pack-target-docker_{os}_{arch}.tar.gz",
      "sha256": "20fb7888…"
    }
  }
}
```

A source is:

|                                            |                                                                                                                                                  |
|--------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| `https://…/pack-target-docker_{os}_{arch}` | the program itself                                                                                                                               |
| `https://…/….tar.gz`, `….tgz`, `….zip`     | an archive that holds `pack-target-<name>`, wherever in it                                                                                       |
| `oci://host/repo:tag`                      | an OCI artifact with the file `pack-target-<name>` ([registry.md](registry.md#publishing-a-target)); its options are beside the `url`, see below |
| `http://…`                                 | the same, in the clear — a mirror inside                                                                                                         |

A source is the URL alone, or the URL with its options, each target's for itself:

```yaml
# release.yaml
targets:
  docker:
    url: oci://localhost:5001/targets/docker:0.1.0-{os}-{arch}
    plain_http: true                 # the registry speaks http, not https
  k8s:
    url: oci://private.example/targets/k8s:0.1.0-{os}-{arch}
    username_env: PRIVATE_USER       # the variables that hold the credentials, never the credentials
    password_env: PRIVATE_TOKEN
  ansible: oci://registry.example/targets/ansible:0.1.0-{os}-{arch}
```

A source that names no variables is read anonymously: the credentials of one never reach another's registry.

`{os}` and `{arch}` stand for GOOS and GOARCH of the machine pack runs on, so one line serves a team with both. A
program
that is in `.pack/bin` with the locked checksum is not downloaded again. A source that gives another checksum than the
lock has is refused (and the program removed) — `pack refresh --upgrade` takes the new one. A target with no source in
`release.yaml` needs `pack-target-<name>` to be in `.pack/bin` already. `memory` is in pack and needs nothing.

`pack` looks for the programs in the directories `PACK_TARGET_PATH` lists, by default **`.pack/bin` of the release it
runs in** — nowhere else (not beside itself, not on `PATH`). The target is stopped when the command ends.

## Where a target runs: the runtime

A target's program runs on the machine pack runs on (`native`, the default), or from the target's **image in a
container** (`docker`): the image carries the program and all it needs — ansible with its collections, the docker
CLI — so the machine needs pack and podman or docker, nothing else.

```yaml
# release.yaml
targets:
  ansible:
    image: oci://registry.example/targets/ansible:0.2.0   # beside url:, or instead of it
```

```yaml
# env.yaml
target: ansible
runtime:
  type: docker                       # native | docker
  network: host                      # the container's network; host — the machine's (none: a port on 127.0.0.1);
                                     # under Docker Desktop (Mac, Windows) host is its VM's network, not the machine's
  mounts:                            # source:destination[:ro|:rw], ~/ the home, both absolute
    - ~/.ssh/known_hosts:/home/pack/.ssh/known_hosts:ro
  ssh_agent: true                    # the machine's agent in the container (Docker Desktop's own on a Mac)
  env:                               # variables passed in by name; their values never in a file or a command line
    - ANSIBLE_VAULT_PASSWORD
  user: host                         # host — pack's uid:gid and the groups of the sockets mounted (Linux's default);
                                     # image — the image's user (the default under Docker Desktop)
```

- `pack refresh` locks the digest the image's tag means (`release.lock.json`: `targets.<name>.image`, `digest`) and every
  command runs that digest, whatever the tag becomes; `--upgrade` takes the new one. The container runtime pulls it.
- Before it runs, the image's labels must say it is the target's and speaks pack's protocol:
  `io.horchestra.pack.target=<name>`, `io.horchestra.pack.protocol=pack.target.v1`. Its entrypoint is
  `pack-target-<name>`, listening where `PACK_TARGET_ADDRESS` says (port 7353), asking for `PACK_TARGET_TOKEN`; its
  `HOME` is `/home/pack`.
- The container's home is the release's: pack mounts `.pack/home/<name>` at `/home/pack`, so what a target keeps in
  `~` — the ansible target's state, the credentials it provisioned — outlives the container. A mount into the home
  (`~/.ssh/id:/home/pack/.ssh/id:ro`) lies over it.
- On Linux a mount is the files as they are on the machine: the program runs as pack's user (`user: host`), with the
  group of every socket mounted (the engine's, the agent's), so the home, a key of `0600` and the socket are its. A
  rootless runtime gets the same its own way: podman `--userns=keep-id --group-add keep-groups`, docker the container's
  root, which is the user. Docker Desktop gives a mount to any user and its engine's socket to root alone: there the
  image's user runs it (`user: image`, the default off Linux; Docker Desktop for Linux wants it said). On Linux with
  `user: image` the program cannot write the home pack made for it.
- The images of the targets of github.com/horchestra/targets are `ghcr.io/horchestra/targets/<name>:<version>`
  (`make images` builds them): docker (the docker CLI; mount the engine's socket), ansible (ansible-core, the ssh
  client; mount the key or pass the agent), k8s (the program alone; mount the kubeconfig).
- The container is labelled with the installation, the target and the pack that runs it; a container a pack of this
  machine left behind (killed, its process gone) is removed when the next one starts — on Linux and macOS, where pack
  can tell its process is gone; elsewhere it stays for `docker rm`. `PACK_CONTAINER_RUNTIME` names
  the runtime; unset — podman, else docker.
- The runtime is pack's, not the target's: the target is configured without it. A target in pack itself (`memory`)
  runs in pack's process and has none. An entry of `targets:` in `env.yaml`
  has a runtime of its own (`targets: {edge: {target: docker, runtime: {type: docker}}}`).

A target is a module of its own so that what it pulls in (client-go, the docker CLI's helpers, an SSH library) is not
the
core's. It is tested on its own, in its repository: `cd <name> && go test ./...`.

## Writing one

A target in Go is a `target.Factory` and a `main`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/horchestra/pack/pkg/target/remote"
)

func main() {
	if err := remote.Serve("mytarget", factory, remote.CapRecord); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "pack-target-mytarget:", err)
		os.Exit(1)
	}
}
```

`factory(installation, settings)` makes the installation (`target.Installation`) for the environment's settings —
refuse a setting it does not know with `target.Settings(set).Check(...)`:

|                                                  |                                                                                                                                                                                                                                                                                                                                                                                                                    |
|--------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `Namespace() string`                             | the scope it installs into; empty where it has none                                                                                                                                                                                                                                                                                                                                                                |
| `Resolve(key, render.Placement)`                 | what the key is on the target: `Service`, `Address`, `Instances` (when the environment gave replicas), `Reach`. `Placement` carries `Replicas`, `Resources`, `Env`, `Labels`, `Annotations`, `Reach` and `Raw` — the rest of the key's entry in `env.yaml`, which only the target reads. Resolve is called for the installed version and the wanted one before any step; keep what the steps need of the placement |
| `Execute(ctx, target.Step, events func(string))` | one step: `apply`, `wait`, `stop`, `hook`, `remove`, with the key's unit in the wanted and the installed version and the credentials. It must be safe to repeat: a saga cut short repeats what it cannot tell was done. `step.Rollback` marks a step that undoes an earlier one                                                                                                                                    |
| `target.Recorder` (optional, `CapRecord`)        | keeps pack's record of the installation in the target's own system: `state: target`                                                                                                                                                                                                                                                                                                                                |
| `target.Observer` (optional, `CapObserve`)       | says what it holds by key (image, replicas) so that a change made behind pack's back is seen as drift                                                                                                                                                                                                                                                                                                              |
| `io.Closer` (optional)                           | released when the target stops — a tunnel, a connection                                                                                                                                                                                                                                                                                                                                                            |

## The protocol

`proto/target.proto` (`pack.target.v1`) is the seam, for a target in any language. pack tells the process in its
environment where to listen and what to demand — `PACK_TARGET_ADDRESS` (`unix:///<path>`, or `tcp://<host>:<port>` for
an image) and `PACK_TARGET_TOKEN`, carried by every call as the metadata `pack-target-token` — and closes its stdin when
it is done: that is the target's signal to exit.

```
Handshake(protocol) → protocol, target, capabilities [record, observe]
Configure(installation, settings) → namespace
Resolve(key, placement_json) → service, address, instances, reach
ReadInstalled / WriteInstalled / DeleteInstalled        (record)
Observe → held[key] = {image, replicas}                  (observe)
Execute(step, unit_json, previous_json, credentials_json) → stream of events; the last one says done or the error
```

What the render knows of a key travels as JSON — `render.Unit`, `render.Placement`, `[]render.Credential` — because
they grow and a number in them must stay an integer where it was one. `make proto` regenerates the Go stubs.
