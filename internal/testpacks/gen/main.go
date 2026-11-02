// Command gen is the generator of the test packs, as an author's cmd/gen is: sdk.Run over the roots.
package main

import (
	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/sdk"
)

func main() { sdk.Run(&openbao.OpenBao{}) }
