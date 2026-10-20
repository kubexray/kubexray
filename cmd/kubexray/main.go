// Command kubexray serves a live, animated map of a Kubernetes cluster.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kubexray/kubexray/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	fmt.Fprintln(os.Stderr, "kubexray: not implemented yet, see ROADMAP.md")
	os.Exit(1)
}
