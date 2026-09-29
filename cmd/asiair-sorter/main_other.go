//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "asiair-sorter — программа с окном, работает только в Windows")
	os.Exit(1)
}
