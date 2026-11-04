// Command gen is the generator of the test packs, as an author's cmd/gen is: sdk.Run over the roots.
package main

import (
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/internal/testpacks/openbao"
)

func main() { sdk.Run(&openbao.OpenBao{}) }
