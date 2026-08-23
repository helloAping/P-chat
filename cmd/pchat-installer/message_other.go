//go:build !windows

package main

import (
	"fmt"
	"os"
)

func showInstallerError(title string, message string) {
	fmt.Fprintf(os.Stderr, "%s\n%s\n", title, message)
}
