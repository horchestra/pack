// Package format is the words a pack is written in, shared by whoever writes one (sdk) and whoever reads one
// (bundle): the types of its artifacts and the annotations that name them.
package format

// Artifact types of a pack's members, as the pack files declare them.
const (
	ArtifactPack     = "application/vnd.pack.pack.v1+json"
	ArtifactSchema   = "application/vnd.pack.schema.v1+json"
	ArtifactTemplate = "application/vnd.pack.template.v1"

	AnnotationName    = "pack.name"
	AnnotationVersion = "pack.version"
	// AnnotationImage marks the item of a pack file that is the image the pack runs, among the items that mount
	// packs it holds: both are oci:// items.
	AnnotationImage = "pack.image"
)

// Names of the files of a pack's directory.
const (
	SchemaFile   = "schema.json"
	PackFile     = "pack.yaml"
	TemplatesDir = "templates"
)
