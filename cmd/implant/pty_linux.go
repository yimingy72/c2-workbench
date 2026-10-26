//go:build linux

package main

// Pseudo-terminal support for the interactive shell.
//
// Without a pty the shell gets pipes, tcsetpgrp fails (ENOTTY) and it degrades
// to "no job control": Ctrl-C cannot signal the foreground child, readline
// stays off (no tab completion, no arrow keys) and full-screen programs such as
// top/vi/less misbehave. Allocating /dev/ptmx gives the shell a real controlling
// terminal, exactly like an ssh session.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	ioctlTIOCSPTLCK = 0x40045431 // set (unlock) the pty lock
	ioctlTIOCGPTN   = 0x80045430 // read the pty slave number
	ioctlTCGETS     = 0x5401
	ioctlTCSETS     = 0x5402
	ioctlTIOCSWINSZ = 0x5414
)

// Kernel struct termios (asm-generic): four flag words, the line discipline,
// NCCS=19 control characters and the two speeds. Getting this layout wrong is
// how "Ctrl-C does nothing" happens: ISIG lives in c_lflag.
type termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [19]uint8
	Ispeed uint32
	Ospeed uint32
}

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

const (
	iflagICRNL   = 0x00000100
	iflagIXON    = 0x00000400
	iflagBRKINT  = 0x00000002
	iflagIUTF8   = 0x00004000
	oflagOPOST   = 0x00000001
	oflagONLCR   = 0x00000004
	lflagISIG    = 0x00000001
	lflagICANON  = 0x00000002
	lflagECHO    = 0x00000008
	lflagECHOE   = 0x00000010
	lflagECHOK   = 0x00000020
	lflagECHOCTL = 0x00000200
	lflagECHOKE  = 0x00000800
	lflagIEXTEN  = 0x00008000
	ccVINTR      = 0
	ccVQUIT      = 1
	ccVERASE     = 2
	ccVKILL      = 3
	ccVEOF       = 4
	ccVTIME      = 5
	ccVMIN       = 6
	ccVSTART     = 8
	ccVSTOP      = 9
	ccVSUSP      = 10
)

// setupTTY writes the terminal parameters an interactive session expects, so
// ^C/^Z produce signals, lines are canonical and the shell echoes what is typed.
func setupTTY(f *os.File) {
	var t termios
	if err := ioctl(f.Fd(), ioctlTCGETS, unsafe.Pointer(&t)); err != nil {
		return
	}
	t.Iflag |= iflagICRNL | iflagIXON | iflagBRKINT | iflagIUTF8
	t.Oflag |= oflagOPOST | oflagONLCR
	t.Lflag |= lflagISIG | lflagICANON | lflagECHO | lflagECHOE | lflagECHOK |
		lflagECHOCTL | lflagECHOKE | lflagIEXTEN
	t.Cc[ccVINTR] = 3    // ^C
	t.Cc[ccVQUIT] = 28   // ^\
	t.Cc[ccVERASE] = 127 // DEL
	t.Cc[ccVKILL] = 21   // ^U
	t.Cc[ccVEOF] = 4     // ^D
	t.Cc[ccVSTART] = 17  // ^Q
	t.Cc[ccVSTOP] = 19   // ^S
	t.Cc[ccVSUSP] = 26   // ^Z
	t.Cc[ccVMIN] = 1
	t.Cc[ccVTIME] = 0
	_ = ioctl(f.Fd(), ioctlTCSETS, unsafe.Pointer(&t))
	ws := winsize{Row: 40, Col: 120}
	_ = ioctl(f.Fd(), ioctlTIOCSWINSZ, unsafe.Pointer(&ws))
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// attachPTY gives cmd a pty as its controlling terminal and returns the master
// side plus a cleanup to run after Start. The caller starts the process.
func attachPTY(cmd *exec.Cmd) (master *os.File, cleanup func(), err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, func() {}, err
	}
	unlock := int32(0)
	if err := ioctl(m.Fd(), ioctlTIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		_ = m.Close()
		return nil, func() {}, err
	}
	var ptn int32
	if err := ioctl(m.Fd(), ioctlTIOCGPTN, unsafe.Pointer(&ptn)); err != nil {
		_ = m.Close()
		return nil, func() {}, err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, func() {}, err
	}

	setupTTY(slave)

	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	// Setsid + Setctty(Ctty: 0) makes the slave the child's controlling tty;
	// Ctty refers to the descriptor number inside the child, and stdin is 0.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	cleanup = func() { _ = slave.Close() }
	return m, cleanup, nil
}
