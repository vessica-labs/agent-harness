package main

import (
	"fmt"
	"os"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-harness:", err)
		os.Exit(1)
	}
}
