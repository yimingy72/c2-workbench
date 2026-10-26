//go:build !linux

package main

// Non-Linux platforms keep the pipe-based shell. Windows would need ConPTY
// (winpty-style API) and macOS a separate TIOCPTYGNAME dance; neither is worth
// the surface today, and pipes are enough for line-oriented commands there.

import (
	"errors"
	"os"
	"os/exec"
)

func attachPTY(cmd *exec.Cmd) (*os.File, func(), error) {
	return nil, func() {}, errors.New("no pty support on this platform")
}
