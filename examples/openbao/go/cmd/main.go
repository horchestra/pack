// Command openbao is the generator of the openbao pack and of every pack it
// holds: one run writes the whole tree oci-packer packs.
//
//	go run ./examples/openbao/go/cmd generate -o out
package main

import (
	"github.com/horchestra/pack/sdk"

	openbao "github.com/horchestra/pack/examples/openbao/go"
)

func main() { sdk.Run(&openbao.OpenBao{}) }
