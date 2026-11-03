// Package openbao is a pack that holds another: tembo is an application of its own, and optional here.
package openbao

import (
	"fmt"

	"github.com/horchestra/pack/internal/testpacks/tembo"
	"github.com/horchestra/pack/sdk"
	packschema2 "github.com/horchestra/pack/sdk/packschema"
)

// OpenBao is the pack: the application's configuration, and the pack it may be installed with.
type OpenBao struct {
	Bao      Config        `json:"openbao"`
	Postgres *tembo.Config `json:"postgres,omitempty"`
}

// Config is the part of OpenBao's own configuration the test needs.
type Config struct {
	UI       bool           `json:"ui,omitempty"`
	LogLevel string         `json:"log_level,omitempty"`
	APIAddr  string         `json:"api_addr"`
	Storage  []StorageBlock `json:"storage,omitempty"`
}

// StorageBlock is one storage backend, e.g. storage "postgresql".
type StorageBlock struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config,omitempty"`
}

func (o *OpenBao) SetDefaults() { o.Bao.LogLevel = "info" }

// The storage of the embedded postgres is derived, not asked: the formula
// fills it when the operator named postgres and wrote no storage himself.
func (o *OpenBao) PackCompute() map[string]string {
	pg := packschema2.Dotted(o, &o.Postgres)
	return map[string]string{
		packschema2.Pointer(o, &o.Bao.Storage): fmt.Sprintf(
			`has(pack.values.%[1]s) ? [{'type': 'postgresql', 'config': {'connection_url': 'postgres://' + pack.keys.%[1]s.service + ':5432/openbao'}}] : null`, pg),
	}
}

// ... and the other half of the fact: with the embedded postgres chosen,
// storage is the formula's, never the operator's.
func (o *OpenBao) PackRules() map[string][]packschema2.Rule {
	pg, storage := packschema2.Dotted(o, &o.Postgres), packschema2.Dotted(o, &o.Bao.Storage)
	return map[string][]packschema2.Rule{
		packschema2.Pointer(o, &o.Bao.Storage): {{
			ID:      "storage-from-postgres",
			Rule:    fmt.Sprintf("!has(pack.values.%s) || !has(values.%s) || size(values.%[2]s) == 0", pg, storage),
			Message: "storage is filled from the embedded postgres and must not be set",
		}},
	}
}

// Version and Image are what the pack is of; a program that publishes the pack for a real image sets them first.
var (
	Version = "2.1.0"
	Image   = "images/openbao:2.1.0"
)

func (*OpenBao) Meta() sdk.Meta {
	return sdk.Meta{
		Name:        "openbao",
		Version:     Version,
		Description: "secrets",
		License:     "MPL-2.0",
		Image:       Image,
		Annotations: map[string]string{"example.org/tier": "core"},
	}
}

func (o *OpenBao) PackRoot() packschema2.Root {
	return packschema2.Root{
		Ports:      map[string]int{"api": 8200},
		Content:    []packschema2.File{{Path: "/etc/openbao/config.json", Values: "openbao"}},
		Readiness:  &packschema2.Readiness{HTTP: "api/v1/sys/health"},
		Savepoints: []string{packschema2.Dotted(o, &o.Postgres)},
	}
}
