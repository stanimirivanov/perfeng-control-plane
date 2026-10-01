// Command repo-check validates repository documentation and contribution policy.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "repository root to validate")
	flag.Parse()

	violations, err := checkRepository(*root)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "repo-check: %v\n", err)
		os.Exit(2)
	}
	for _, item := range violations {
		_, _ = fmt.Fprintln(os.Stderr, item)
	}
	if len(violations) != 0 {
		_, _ = fmt.Fprintf(os.Stderr, "repo-check: found %d policy violation(s)\n", len(violations))
		os.Exit(1)
	}

	_, _ = fmt.Println("repo-check: repository policy checks passed")
}
