// Package oci reads files published as OCI artifacts.
package oci

import (
	"context"

	internal "github.com/horchestra/pack/internal/oci"
)

// Options of a registry and the artifact platform to read.
type Options = internal.Options

// PullFile reads one file out of an OCI artifact published with oci-packer.
func PullFile(ctx context.Context, ref, name string, o Options) ([]byte, error) {
	return internal.PullFile(ctx, ref, name, o)
}
