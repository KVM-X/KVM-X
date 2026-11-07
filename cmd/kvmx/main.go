// Command kvmx gets a working VM environment on machines without nested virtualization.
package main

import (
	"os"

	"github.com/KVM-X/KVM-X/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, os.Stdin, os.Stdout, os.Stderr))
}
