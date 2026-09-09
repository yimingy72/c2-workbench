package listener

import (
	"net"
	"testing"
	"time"

	"c2-platform/internal/protocol"
	"c2-platform/internal/session"
)

func TestRegisterSessionAppears(t *testing.T) {
	h := session.NewHub(nil)
	m := NewManager(h)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	spec, err := m.Start(Spec{Name: "t", Bind: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(spec.ID)

	c, err := net.DialTimeout("tcp", lnAddr(port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	reg := protocol.Register{Ver: 1, PID: 7, Hostname: "box", Username: "u", OS: "windows", Arch: "x64", IP: "10.0.0.2"}
	if _, err := c.Write(protocol.Encode(protocol.CmdRegister, reg.Marshal())); err != nil {
		t.Fatal(err)
	}
	f, err := protocol.Decode(c)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != protocol.RspOK || len(f.Payload) == 0 {
		t.Fatalf("ok frame %+v", f)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(h.List()) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sessions %d", len(h.List()))
}

func lnAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", itoa(port))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
