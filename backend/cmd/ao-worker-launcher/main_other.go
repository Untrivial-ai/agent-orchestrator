//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() { fmt.Fprintln(os.Stderr, "ao-worker-launcher is supported only on Windows"); os.Exit(1) }
