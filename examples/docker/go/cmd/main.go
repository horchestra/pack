// Command docker is the generator of the docker pack.
//
//	go run ./examples/docker/go/cmd generate -o out
package main

import (
	"github.com/horchestra/pack/sdk"

	docker "github.com/horchestra/pack/examples/docker/go"
)

func main() { sdk.Run(&docker.Docker{}) }
