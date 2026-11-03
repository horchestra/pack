package target

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Facts are what a target tells of where it installs, as it is: its hosts — the machines, the nodes, the engines —
// and what is the target's as a whole. A target fills what it can; a fact it cannot tell is left out. Read only:
// telling the facts changes nothing.
type Facts struct {
	At    time.Time            `json:"at"`              // when they were told
	Hosts map[string]HostFacts `json:"hosts,omitempty"` // by the name the target knows the host by
	// Unreachable are the hosts that told nothing, and why: the others' facts are still told.
	Unreachable map[string]string `json:"unreachable,omitempty"`
	// Kubernetes is the version of the cluster's API; StorageClasses its storage classes, the default first.
	Kubernetes     string   `json:"kubernetes,omitempty"`
	StorageClasses []string `json:"storage_classes,omitempty"`
}

// HostFacts are the facts of one host.
type HostFacts struct {
	Address   string `json:"address,omitempty"`
	OS        string `json:"os,omitempty"`         // debian, fedora, ubuntu, linux …
	OSVersion string `json:"os_version,omitempty"` // 13, 42 …
	Arch      string `json:"arch,omitempty"`       // amd64, arm64
	CPUs      int    `json:"cpus,omitempty"`
	MemoryMB  int64  `json:"memory_mb,omitempty"`
	Disks     []Disk `json:"disks,omitempty"` // by mount point
	Ports     []Port `json:"ports,omitempty"` // listening
	// Runtimes are what runs workloads there: docker, podman, containerd, systemd.
	Runtimes []string `json:"runtimes,omitempty"`
	// Docker is the engine's version where there is one.
	Docker string   `json:"docker,omitempty"`
	DNS    []string `json:"dns,omitempty"` // the resolvers
	// Time is the host's clock when it was asked: a host far off the others' breaks what signs or expires.
	Time *time.Time `json:"time,omitempty"`
}

// Disk is a mounted filesystem.
type Disk struct {
	Mount  string `json:"mount"`
	SizeMB int64  `json:"size_mb"`
	FreeMB int64  `json:"free_mb"`
}

// Port is a port something listens on.
type Port struct {
	Port    int    `json:"port"`
	Proto   string `json:"proto,omitempty"`   // tcp, udp
	Process string `json:"process,omitempty"` // what listens, when the target can tell
}

// ErrNoFacts is a target that does not tell facts.
var ErrNoFacts = errors.New("the target tells no facts")

// Discoverer is a target that tells the facts of where it installs. hosts names the hosts to ask; empty — all of
// them.
type Discoverer interface {
	Discover(ctx context.Context, hosts []string) (*Facts, error)
}

// Map is the facts as a formula reads them: the JSON's own names (facts.hosts.db1.memory_mb).
func (f *Facts) Map() map[string]any {
	out := map[string]any{}
	if f == nil {
		return out
	}
	b, err := json.Marshal(f)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}
