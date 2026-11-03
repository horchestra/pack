// Package docs holds the documentation of pack, for pack itself to quote: pack author ref.
package docs

import _ "embed"

// Authoring is authoring.md: how a pack is written.
//
//go:embed authoring.md
var Authoring string
