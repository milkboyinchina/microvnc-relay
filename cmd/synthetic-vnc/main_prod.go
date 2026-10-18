//go:build !dev

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "synthetic-vnc is a development-only binary; rebuild with -tags dev")
	os.Exit(1)
}
