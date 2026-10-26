//go:build windows

package main

// Windows has no tty signals to inherit; the console host handles Ctrl-C.
func resetInheritedSignals() {}
