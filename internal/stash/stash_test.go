package stash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPutListFileDelete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	it, err := s.Put("s1", "a.txt", `C:\a.txt`, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	list := s.List("s1")
	if len(list) != 1 || list[0].Name != "a.txt" {
		t.Fatalf("%+v", list)
	}
	got, b, err := s.File(it.ID)
	if err != nil || string(b) != "hello" || got.Remote != `C:\a.txt` {
		t.Fatalf("%s %q %v", got.Remote, b, err)
	}
	if err := s.Delete(it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, it.ID+".bin")); !os.IsNotExist(err) {
		t.Fatal("bin still there")
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if _, err := s.Put("s1", "a.txt", "a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("s2", "b.txt", "b", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	if len(s.List("s1")) != 0 || len(s.List("s2")) != 1 {
		t.Fatalf("s1=%d s2=%d", len(s.List("s1")), len(s.List("s2")))
	}
}
