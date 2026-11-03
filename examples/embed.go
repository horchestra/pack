// Package examples holds the sources of the example packs, for pack itself to show: pack author example.
package examples

import (
	"embed"
	"strings"
)

// Sources are the example packs, in the three languages: <kind>/<lang>/… — the sources, their content/ and docs/.
//
//go:embed postgres/go postgres/python postgres/java openbao/go openbao/python openbao/java docker/go docker/python docker/java
var Sources embed.FS

// IsSource says whether a file of an example's directory is the pack's source, not a test of it.
func IsSource(name string) bool {
	switch {
	case strings.HasSuffix(name, "_test.go"), strings.HasPrefix(name, "test_"), strings.HasSuffix(name, "Test.java"):
		return false
	}
	return strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".java")
}
