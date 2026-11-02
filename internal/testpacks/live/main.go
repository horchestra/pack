// Command live generates the test packs for the images that are really published: openbao 2.6.5 (with tembo
// 0.2.0 inside), whose images were copied to the registry the packs are pushed to.
package main

import (
	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/sdk"
)

func main() {
	openbao.Version = "2.6.5"
	openbao.Image = "oci://images/openbao:2.6.5"
	sdk.Run(&openbao.OpenBao{})
}
