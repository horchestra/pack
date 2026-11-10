// Package postgres is a pack written the way an author writes one: a single-instance PostgreSQL with an install
// hook (the extensions every database gets, after the first start) and a major-version upgrade hook (the saga runs
// pg_upgrade with the component stopped, and takes it back if it fails). The same pack is written in Python and
// Java next to this one; all three generate the same artifacts (examples/postgres/golden). The pack is a library:
// the generator is cmd/, and a pack that holds this one imports it (examples/openbao/go does).
//
//	go run ./examples/postgres/go/cmd generate -o out
package postgres

import (
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Postgres is the pack. The settings are the application's own: the json tag is the key in the schema, a field
// without omitempty is required.
type Postgres struct {
	Database  string    `json:"database" env:"POSTGRES_DB"` // required: no omitempty
	Superuser Superuser `json:"superuser"`
	// Conf is postgresql.conf.
	Conf Conf `json:"conf"`
	// PGDG and PGDGKey are where the packages of PGDG come from on a host: upstream by default, a mirror (oci-proxy of the
	// ansible target) when the hosts do not reach it.
	PGDG    string `json:"pgdg,omitempty"`
	PGDGKey string `json:"pgdg_key,omitempty"`
	// InitExtensions are the extensions every database gets.
	InitExtensions []string `json:"init_extensions,omitempty"`
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
	p.PGDG = "https://apt.postgresql.org/pub/repos/apt"
	p.PGDGKey = "https://www.postgresql.org/media/keys/ACCC4CF8.asc"
	p.InitExtensions = []string{"pg_stat_statements", "pgcrypto"}
}

func (*Postgres) Meta() sdk.Meta {
	return sdk.Meta{
		Name:        "postgres",
		Version:     "16.4",
		Description: "PostgreSQL",
		License:     "PostgreSQL",
		Image:       "images/postgres:16.4",
	}
}

func (*Postgres) PackRoot() packschema.Root {
	return packschema.Root{
		Ports: map[string]int{"pg": 5432},
		// the script of the install hook is rendered: a hook is a container of its own and is told where the server is
		Content: []packschema.File{
			{Path: "/etc/postgresql/postgresql.conf", Values: "conf", Format: "properties"},
			{Path: "/opt/pack/init-extensions.sh", Template: "init-extensions.sh", Mode: "0755"},
			{Path: "/opt/pack/restore-15.sh", Source: "restore-15.sh", Mode: "0755"},
		},
		Data:      map[string]packschema.Volume{"data": {MountPath: "/var/lib/postgresql/data", Size: "10Gi"}},
		Minimum:   map[string]string{"cpu": "500m", "memory": "512Mi"},
		Readiness: &packschema.Readiness{TCP: "pg"},
		Upgrade:   &packschema.Upgrade{From: ">=15"},
		// On a host the same application lives elsewhere, by OS; the version is the OS's, not Meta's: PGDG's 16 on Debian,
		// what Fedora ships there.
		Packages: map[string]packschema.OSInstall{
			"debian": {
				Prepare: []string{"ca-certificates"},
				Repo: []packschema.File{
					{Path: "/etc/apt/keyrings/pgdg.asc", URL: "{{ .pgdg_key }}", Mode: "0644"},
					{Path: "/etc/apt/sources.list.d/pgdg.sources", Template: "host-pgdg.sources", Mode: "0644"},
				},
				Names: []string{"postgresql-16"},
				Content: []packschema.File{
					{Path: "/etc/postgresql/16/main/conf.d/pack.conf", Template: "host-postgresql.conf", Mode: "0644"},
					{Path: "/etc/postgresql/16/main/pg_hba.conf", Source: "host-pg_hba.conf", Mode: "0640"},
					{Path: "/opt/pack/init-extensions.sh", Template: "host-init.sh", Mode: "0700"},
				},
				Services: []string{"postgresql@16-main"},
			},
			"fedora": {
				Names:    []string{"postgresql-server", "postgresql-contrib"},
				Commands: [][]string{{"sh", "-c", "test -f /var/lib/pgsql/data/PG_VERSION || postgresql-setup --initdb"}},
				Content: []packschema.File{
					{Path: "/var/lib/pgsql/data/postgresql.auto.conf", Template: "host-postgresql.conf", Mode: "0644"},
					{Path: "/var/lib/pgsql/data/pg_hba.conf", Source: "host-pg_hba.conf", Mode: "0644"},
					{Path: "/opt/pack/init-extensions.sh", Template: "host-init.sh", Mode: "0700"},
				},
				Services: []string{"postgresql"},
			},
		},
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

// PackActions are what an operator runs on the installed postgres besides an apply: a dump, which only reads, and a
// restore over the database, which the pack never lets run unasked. pg_dump and pg_restore read the server and the
// password from the instance's own socket, where the action runs; a dump under the data volume outlives it.
func (*Postgres) PackActions() []packschema.Action {
	return []packschema.Action{{
		Name:    "backup",
		Command: []string{"pg_dump", "--username={{ .user }}", "--format={{ .format }}", "--file={{ .to }}", "--dbname={{ .database }}"},
		Timeout: "1h",
		Params: []packschema.Param{
			{Name: "to", Type: "string", Required: true},
			{Name: "format", Type: "string", Default: "custom", Enum: []any{"custom", "plain", "directory"}},
			{Name: "database", Type: "string", Default: "postgres"},
			{Name: "user", Type: "string", Default: "postgres"},
		},
	}, {
		Name:     "restore",
		Command:  []string{"pg_restore", "--username={{ .user }}", "--clean", "--if-exists", "--dbname={{ .database }}", "{{ .from }}"},
		Timeout:  "1h",
		Mutating: true,
		Approve:  true,
		Params: []packschema.Param{
			{Name: "from", Type: "string", Required: true},
			{Name: "database", Type: "string", Default: "postgres"},
			{Name: "user", Type: "string", Default: "postgres"},
		},
	}}
}

// PackCompute a quarter of the memory limit, in MB — the formula fills the leaf when the operator did not write
// it. The key of the map is the field's pointer, found from the field itself.
func (p *Postgres) PackCompute() map[string]string {
	return map[string]string{
		packschema.Pointer(p, &p.Conf.SharedBuffers): "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'",
	}
}
