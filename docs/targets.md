# Targets

A **target** is the system a pack is installed on. The core knows none by name: it renders a pack for one target (names,
addresses and instances of the keys end up inside config files), plans the saga and hands the target one step at a time.
A target says what the environment's placement of a key means on its system and performs the steps in it.

This repository has three, each a module of its own with its own `go.mod` and `main.go`, built into a program that pack
starts and speaks to over gRPC:

| `target:` | program               |                                                                                                     |
|-----------|-----------------------|-----------------------------------------------------------------------------------------------------|
| `docker`  | `pack-target-docker`  | [Docker engines](../targets/docker/README.md): one, or several hosts, over the CLI or an SSH tunnel |
| `ansible` | `pack-target-ansible` | [classic hosts](../targets/ansible/README.md): packages, files and systemd services over SSH        |
| `k8s`     | `pack-target-k8s`     | [Kubernetes](../targets/k8s/README.md): StatefulSets, Jobs, Services, Secrets, with client-go       |
| `memory`  | (in pack)             | runs nothing; keeps what it was told in the process — for plans, templates and tests                |

## Using one

```
(cd targets/docker && go build -o ~/bin/pack-target-docker .)
```

```yaml
# env.yaml
target: docker
```

`pack` looks for `pack-target-<name>` in the directories `PACK_TARGET_PATH` lists, beside itself, then on `PATH`. A
`provider:` setting names another way: a binary's path, or `oci://host/repo:tag`, a container image that podman or
docker runs (for a target written in a language whose runtime the machine lacks; the port is published on 127.0.0.1
only). The target is stopped when the command ends.

A target is a module of its own so that what it pulls in (client-go, the docker CLI's helpers, an SSH library) is not
the
core's. It is tested on its own: `cd targets/<name> && go test ./...` — `go test ./...` of the root does not reach it.

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
