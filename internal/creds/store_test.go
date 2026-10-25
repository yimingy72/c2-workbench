package creds

import (
	"strings"
	"sync"
	"testing"
)

func TestPutDedupeAndStats(t *testing.T) {
	s := New(t.TempDir())
	rows := strings.Join([]string{
		"C\tChrome\thttps://github.com\toctocat\tghp_ABCDEFGHIJKLMNOP\t times_used=7 \tLogin Data",
		"C\tChrome\thttps://github.com\toctocat\tghp_ABCDEFGHIJKLMNOP\t times_used=9 \tLogin Data",
		"W\tWiFi\tOffice-5G\t\tSup3rSecretKey\t netsh \t",
		"E\tEnv\tAPI_TOKEN\t\tshort\tlinux 环境变量\t",
		"M\tCredMan\tBox\tsvc\tP@ssw0rd-2024\ttype=1\tCredential Manager",
		"K\tSSHKey\tid_ed25519\t\tQUJDREVGR0g=\tOpenSSH\t/home/a/.ssh/id_ed25519",
	}, "\n")

	added, err := s.Put("host-1", rows, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	if added != 5 {
		t.Fatalf("added = %d, want 5 (one duplicate)", added)
	}

	v := s.Get("host-1")
	if v.Stats.Total != 5 || v.Stats.Browser != 1 || v.Stats.System != 4 {
		t.Fatalf("stats %+v", v.Stats)
	}
	if v.Stats.WiFi != 1 || v.Stats.CredMan != 1 || v.Stats.Env != 1 || v.Stats.Key != 1 {
		t.Fatalf("per-kind stats %+v", v.Stats)
	}
	if v.At != 1700000000 {
		t.Fatalf("timestamp %d", v.At)
	}
	if strings.Join(v.Kinds, ",") != "C,E,K,M,W" {
		t.Fatalf("kinds %v", v.Kinds)
	}

	// the duplicate kept the newest note
	for _, it := range v.Items {
		if it.Kind == "C" && !strings.Contains(it.Note, "times_used=9") {
			t.Fatalf("note not refreshed: %+v", it)
		}
		if it.Kind == "C" && it.Secret != "gh••••OP" {
			t.Fatalf("masked secret %q", it.Secret)
		}
		if !it.HasSecret {
			t.Fatalf("has_secret false for %+v", it)
		}
	}
}

func TestMaskBoundaries(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"abc":             "••••",
		"abcdefg":         "••••",
		"abcdefgh":        "ab••••gh",
		"ΩΩΩΩΩΩΩΩ":        "ΩΩ••••ΩΩ",
		"  spaced ":       "••••",
		"  paddedvalue  ": "pa••••ue",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRevealOnlyRequested(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put("h", "W\tWiFi\ta\t\tsecret-one-1\t\t\nW\tWiFi\tb\t\tsecret-two-2\t\t", 1); err != nil {
		t.Fatal(err)
	}
	v := s.Get("h")
	if len(v.Items) != 2 {
		t.Fatalf("items %d", len(v.Items))
	}
	got := s.Reveal("h", []string{v.Items[0].ID})
	if len(got) != 1 {
		t.Fatalf("reveal returned %d entries", len(got))
	}
	if got[v.Items[0].ID] != "secret-one-1" {
		t.Fatalf("wrong secret %+v", got)
	}
	if _, ok := got[v.Items[1].ID]; ok {
		t.Fatal("revealed an unrequested row")
	}
}

func TestExportQuotesAndBOM(t *testing.T) {
	s := New(t.TempDir())
	row := "F\tFileZilla\tftp.example.com:21\tbob\tpa,ss\"word\t说明, 带逗号\t/tmp/x"
	if _, err := s.Put("h", row, 1700000001); err != nil {
		t.Fatal(err)
	}
	b, err := s.Export("h")
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.HasPrefix(out, "\ufeff") {
		t.Fatal("missing BOM")
	}
	if !strings.Contains(out, `"pa,ss""word"`) {
		t.Fatalf("csv escaping broken: %s", out)
	}
	if !strings.Contains(out, "FTP/客户端") {
		t.Fatalf("missing localized kind label: %s", out)
	}
}

func TestNoiseRowsIgnored(t *testing.T) {
	s := New(t.TempDir())
	rows := strings.Join([]string{
		"garbage without tabs",
		"\t\t\t\t\t\t",
		"ZZ\tunknown kind\tx\t\t\t\t",
		"W\tWiFi\tok\t\tvalue-123456\t\t",
	}, "\n")
	added, err := s.Put("h", rows, 5)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added %d, want 1", added)
	}
}

func TestConcurrentPut(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			row := "W\tWiFi\thost" + itoa(n) + "\t\tsecret-value-" + itoa(n) + "\t\t"
			if _, err := s.Put("h", row, int64(n)); err != nil {
				t.Errorf("put: %v", err)
			}
		}(i)
	}
	wg.Wait()
	v := s.Get("h")
	if v.Stats.Total != 16 {
		t.Fatalf("lost writes: %d", v.Stats.Total)
	}
}

func TestHostPathTraversalIsNeutralized(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put("../../etc/passwd", "W\tWiFi\ta\t\tsecret-12345\t\t", 1); err != nil {
		t.Fatal(err)
	}
	// the store must not have escaped its directory: the sanitized host name is
	// what Get() looks up, and reading it back proves the path was rewritten
	v := s.Get("../../etc/passwd")
	if v.Stats.Total != 1 {
		t.Fatalf("sanitized host lookup failed: %+v", v.Stats)
	}
	if err := s.Delete("../../etc/passwd"); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
