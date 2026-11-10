// Package openbao is a pack that holds another: postgres (examples/postgres/go) is an application of its own,
// and optional here. Chosen, it IS the storage: the formula fills openbao.storage from it, and the rule forbids
// writing one by hand — the two halves of one fact.
//
//	go run ./examples/openbao/go/cmd generate -o out
package openbao

import (
	"fmt"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	postgres "github.com/horchestra/pack/examples/postgres/go"
)

// OpenBao is the pack: the application's own configuration, and the pack it may be installed with.
type OpenBao struct {
	Bao      Config             `json:"openbao"`
	Postgres *postgres.Postgres `json:"postgres,omitempty"`
	// Dev runs the server in dev mode — in-memory, unsealed, the config file ignored: the image's own default,
	// for trying the pack out. The command of the pack reads it; anything else runs the rendered config.
	Dev bool `json:"dev,omitempty" env:"BAO_DEV"`
	// ReleaseURL is where a package file of a release comes from: GitHub by default, a mirror (oci-packer proxy) when the
	// hosts do not reach it.
	ReleaseURL string `json:"release_url,omitempty"`
}

func (o *OpenBao) SetDefaults() {
	o.Bao.LogLevel = "info"
	o.Bao.DisableMlock = true // a container cannot mlock
	o.ReleaseURL = "https://github.com/openbao/openbao/releases/download"
}

func (*OpenBao) Meta() sdk.Meta {
	return sdk.Meta{
		Name: "openbao", Version: "2.6.5", Description: "manage, store and distribute sensitive data", License: "MPL-2.0",
		Image: "images/openbao:2.6.5",
	}
}

func (o *OpenBao) PackRoot() packschema.Root {
	return packschema.Root{
		Ports:   map[string]int{"api": 8200, "cluster": 8201},
		Content: []packschema.File{{Path: "/openbao/config/config.json", Values: "openbao"}},
		// The package of each OS reads the config at its own path and is of its own version: Fedora's own, the release's
		// file on Debian, which has no repository; ReleaseURL says where that comes from.
		Packages: map[string]packschema.OSInstall{
			"debian": {
				Prepare:  []string{"ca-certificates", "openssl"},
				Names:    []string{"{{ .release_url }}/v2.6.5/openbao_2.6.5_linux_${os:arch}.deb"},
				Content:  []packschema.File{{Path: "/etc/openbao/openbao.hcl", Values: "openbao", Format: "json", Mode: "0644"}},
				Services: []string{"openbao"},
			},
			"fedora": {
				Names:    []string{"openbao"},
				Content:  []packschema.File{{Path: "/etc/openbao.d/openbao.hcl", Values: "openbao", Format: "json", Mode: "0644"}},
				Services: []string{"openbao"},
			},
		},
		Data:       map[string]packschema.Volume{"file": {MountPath: "/openbao/file", Size: "1Gi"}},
		Readiness:  &packschema.Readiness{HTTP: "api/v1/sys/health"},
		Command:    []string{"/bin/sh", "-c", `if [ "${BAO_DEV:-}" = true ]; then exec docker-entrypoint.sh server -dev -dev-no-store-token; fi; exec docker-entrypoint.sh server`},
		Savepoints: []string{packschema.Dotted(o, &o.Postgres)},
	}
}

// PackCompute the storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
// postgres and wrote no storage himself — the database name and the user are the ones the operator gave postgres,
// the password the placeholder of the credential pack provisions for it (the target resolves ${secret:…}).
func (o *OpenBao) PackCompute() map[string]string {
	pg := packschema.Dotted(o, &o.Postgres)
	return map[string]string{
		packschema.Pointer(o, &o.Bao.Storage): fmt.Sprintf(
			`has(pack.values.%[1]s) ? {'postgresql': {'connection_url': 'postgres://' + pack.values.%[1]s.superuser.name + `+
				`':${secret:' + pack.installation + '-%[1]s-superuser-password/password}@' + pack.keys.%[1]s.instances[0] + ':' + `+
				`string(pack.keys.%[1]s.ports.pg) + '/' + pack.values.%[1]s.database}} : null`, pg),
	}
}

// PackRules PackCompute and the other half of the fact: with the embedded postgres chosen, storage is the formula's, never the
// operator's.
func (o *OpenBao) PackRules() map[string][]packschema.Rule {
	pg, storage := packschema.Dotted(o, &o.Postgres), packschema.Dotted(o, &o.Bao.Storage)
	return map[string][]packschema.Rule{
		packschema.Pointer(o, &o.Bao.Storage): {{
			ID:      "storage-from-postgres",
			Rule:    fmt.Sprintf("!has(pack.values.%s) || !has(values.%s) || size(values.%[2]s) == 0", pg, storage),
			Message: "storage is filled from the embedded postgres and must not be set",
		}},
	}
}
