// validate-registry checks one reviewed runtime registry bundle without starting services.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

type registryLoader func(fs.FS, string) (*registry.ReportPolicyRegistry, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, registry.LoadReportPolicyRegistry))
}

func run(args []string, stdout, stderr io.Writer, load registryLoader) int {
	flags := flag.NewFlagSet("validate-registry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "trusted registry bundle root directory")
	manifest := flags.String("manifest", "registry.json", "bundle manifest path relative to root")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if load == nil || *root == "" || *manifest == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Registry bundle root, relative manifest, and no positional arguments are required.")
		return 2
	}

	if _, err := load(os.DirFS(*root), *manifest); err != nil {
		fmt.Fprintln(stderr, "Registry bundle validation failed.")
		return 1
	}
	fmt.Fprintln(stdout, "Registry bundle is valid.")

	return 0
}
