// Command postgres is the generator of the postgres pack: the pack itself is
// the library next to this directory.
//
//	go run ./examples/postgres/go/cmd generate -o out
package main

import (
	"github.com/horchestra/pack/sdk"

	postgres "github.com/horchestra/pack/examples/postgres/go"
)

func main() { sdk.Run(&postgres.Postgres{}) }
