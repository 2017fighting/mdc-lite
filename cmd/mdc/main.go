// Command mdc is the mdc-lite binary: the server, the config checker and the
// pipeline runner all in one statically linked file.
package main

import (
	"os"

	"github.com/2017fighting/mdc-lite/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
