// Command postgres is a pack written the way an author writes one: a single-instance PostgreSQL with an install hook
// (the extensions every database gets, after the first start) and a major-version upgrade hook (the saga runs
// pg_upgrade with the component stopped, and takes it back if it fails). The same pack is written in Python and
// Java next to this one; all three generate the same artifacts (examples/postgres/golden).
//
//	go run ./examples/postgres/go generate -o out
package main

import (
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Postgres is the pack. The settings are the application's own: the json tag is the key in the schema, a field
// without omitempty is required.
type Postgres struct {
	Database  string    `json:"database"` // required: no omitempty
	Superuser Superuser `json:"superuser"`
	// Conf is postgresql.conf, written from the subtree as properties.
	Conf Conf `json:"conf" file:"/etc/postgresql/postgresql.conf" format:"properties"`
	// The scripts the hooks run are files of content/, shipped as they are.
	InitExtensions string `json:"init_extensions,omitempty" file:"/opt/pack/init-extensions.sh" template:"init-extensions.sh" format:"raw" mode:"0755"`
	Restore15      string `json:"restore_15,omitempty" file:"/opt/pack/restore-15.sh" template:"restore-15.sh" format:"raw" mode:"0755"`
}

type Superuser struct {
	Name     string     `json:"name,omitempty" env:"POSTGRES_USER"`
	Password sdk.Secret `json:"password" env:"POSTGRES_PASSWORD"` // required: the operator names a Secret
}

type Conf struct {
	MaxConnections  int    `json:"max_connections,omitempty"`
	SharedBuffers   string `json:"shared_buffers,omitempty"` // a quarter of the memory limit, unless the operator says
	ListenAddresses string `json:"listen_addresses,omitempty"`
	Port            int    `json:"port,omitempty"`
}

func (p *Postgres) SetDefaults() {
	p.Superuser.Name = "postgres"
	p.Conf.MaxConnections = 100
	p.Conf.ListenAddresses = "*"
	p.Conf.Port = 5432
}

func (*Postgres) Meta() sdk.Meta {
	return sdk.Meta{
		Name: "postgres", Version: "16.4", Description: "PostgreSQL", License: "PostgreSQL",
		Image:    "oci://images/postgres:16.4",
		Artifact: "oci://packs/postgres",
	}
}

func (*Postgres) PackRoot() packschema.Root {
	return packschema.Root{
		Ports:     map[string]int{"pg": 5432},
		Data:      map[string]packschema.Volume{"data": {MountPath: "/var/lib/postgresql/data", Size: "10Gi"}},
		Minimum:   map[string]string{"cpu": "500m", "memory": "512Mi"},
		Readiness: &packschema.Readiness{TCP: "pg"},
		Upgrade:   &packschema.Upgrade{From: ">=15"},
		Hooks: []packschema.Hook{{
			Name: "init-extensions", Phase: "postInstall", Image: "new",
			Command: []string{"/opt/pack/init-extensions.sh"}, Idempotent: true, Timeout: "5m",
		}, {
			Name: "pg-upgrade", Phase: "preUpgrade",
			When:      &packschema.When{From: "<16", To: ">=16"},
			Image:     "new",
			Command:   []string{"/usr/lib/postgresql/16/bin/pg_upgrade", "--old-bindir=/usr/lib/postgresql/15/bin", "--new-bindir=/usr/lib/postgresql/16/bin", "--old-datadir=/var/lib/postgresql/data", "--new-datadir=/var/lib/postgresql/data.16"},
			Exclusive: true, Timeout: "30m",
			Compensate: &packschema.Step{Image: "old", Command: []string{"/opt/pack/restore-15.sh"}},
		}, {
			Name: "analyze", Phase: "postUpgrade", When: &packschema.When{To: ">=16"}, Image: "new",
			Command: []string{"vacuumdb", "--all", "--analyze-in-stages"}, Idempotent: true,
		}},
	}
}

// A quarter of the memory limit, in MB: the formula fills the leaf when the operator did not write it. The key of
// the map is the field's pointer, found from the field itself.
func (p *Postgres) PackCompute() map[string]string {
	return map[string]string{
		packschema.Pointer(p, &p.Conf.SharedBuffers): "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'",
	}
}

func main() { sdk.Run(&Postgres{}) }
