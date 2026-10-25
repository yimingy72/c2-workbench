package browser

import "testing"

func TestPutListDiff(t *testing.T) {
	s := New(t.TempDir())
	first := "F\tChrome\tDefault\tHistory\t1700000000\t100\nV\tChrome\t-\t140.0\t0\t0"
	if _, err := s.Put("pc-1", first, 100); err != nil {
		t.Fatal(err)
	}
	second := "F\tChrome\tDefault\tHistory\t1700000600\t200\nB\tChrome\tDefault\tGo\thttps://go.dev\t0"
	if _, err := s.Put("pc-1", second, 200); err != nil {
		t.Fatal(err)
	}
	list := s.List("pc-1")
	if len(list) != 2 || list[0].At != 200 {
		t.Fatalf("list %+v", list)
	}
	if len(list[0].Rows) != 2 {
		t.Fatalf("rows %+v", list[0].Rows)
	}
	added, removed := Diff(list[1].Rows, list[0].Rows)
	if len(added) != 1 || len(removed) != 1 {
		t.Fatalf("added %v removed %v", added, removed)
	}
	if added[0][:2] != "B\t" || removed[0][:2] != "V\t" {
		t.Fatalf("added %v removed %v", added, removed)
	}
}
