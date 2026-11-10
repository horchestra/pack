// Package docker is a pack that makes a host a Docker host: the engine from Docker's own repositories
// (https://docs.docker.com/engine/install/), its daemon.json and its unit. It has no image: it is for a target that
// installs OS packages. The host is taken to be empty, nothing conflicting is looked for or removed.
//
//	go run ./examples/docker/go/cmd generate -o out
package docker

import (
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Docker is the pack. Mirror is where the packages come from: Docker's own by default, a mirror of the operator's
// (oci-packer proxy) when the hosts do not reach it.
type Docker struct {
	Mirror string `json:"mirror,omitempty"`
	Daemon Daemon `json:"daemon"`
}

// Daemon is /etc/docker/daemon.json.
type Daemon struct {
	LogDriver string  `json:"log-driver,omitempty"`
	LogOpts   LogOpts `json:"log-opts"`
}

type LogOpts struct {
	MaxSize string `json:"max-size,omitempty"`
	MaxFile string `json:"max-file,omitempty"`
}

func (d *Docker) SetDefaults() {
	d.Mirror = "https://download.docker.com/linux"
	d.Daemon.LogDriver = "json-file"
	d.Daemon.LogOpts = LogOpts{MaxSize: "50m", MaxFile: "3"}
}

func (*Docker) Meta() sdk.Meta {
	return sdk.Meta{Name: "docker", Version: "1.0.0", Description: "Docker Engine from OS packages", License: "Apache-2.0"}
}

func (*Docker) PackRoot() packschema.Root {
	engine := []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}
	daemon := packschema.File{Path: "/etc/docker/daemon.json", Values: "daemon", Format: "json", Mode: "0644"}

	return packschema.Root{Packages: map[string]packschema.OSInstall{
		"debian": {
			Prepare: []string{"ca-certificates"},
			Repo: []packschema.File{
				{Path: "/etc/apt/keyrings/docker.asc", URL: "{{ .mirror }}/debian/gpg", Mode: "0644"},
				{Path: "/etc/apt/sources.list.d/docker.sources", Template: "docker.sources", Mode: "0644"},
			},
			Names:    engine,
			Content:  []packschema.File{daemon},
			Services: []string{"docker"},
		},
		"fedora": {
			Repo:     []packschema.File{{Path: "/etc/yum.repos.d/docker-ce.repo", Template: "docker-ce.repo", Mode: "0644"}},
			Names:    engine,
			Content:  []packschema.File{daemon},
			Services: []string{"docker"},
		},
	}}
}
