package main

import (
	"io"
	"os"
)

// Test conveniences over the production entry points.

func runCommand(arguments []string, output io.Writer) error {
	return runCommandWithInput(arguments, output, os.Stdin)
}
