//go:build !windows
// +build !windows

package main

import "fmt"

func main() {
	fmt.Println("mshell desktop build is intended for Windows. Build with GOOS=windows or run build-installer.ps1 on Windows.")
}
