package storage

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestLocalRoundTrip(t *testing.T) {
	l := Local{Dir: t.TempDir()}
	ctx := context.Background()
	n, err := l.Put(ctx, "t1/2026/10/job/parties.csv", strings.NewReader("a,b\n"), "text/csv")
	if err != nil || n != 4 {
		t.Fatalf("%d %v", n, err)
	}
	rc, err := l.Open(ctx, "t1/2026/10/job/parties.csv")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "a,b\n" {
		t.Fatalf("%q", b)
	}
	if _, err := l.Put(ctx, "../escape", strings.NewReader("x"), ""); err == nil {
		t.Fatal("path traversal must fail")
	}
}

func TestLocalDelete(t *testing.T) {
	l := Local{Dir: t.TempDir()}
	ctx := context.Background()
	if _, err := l.Put(ctx, "a/b.csv", strings.NewReader("x"), ""); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, "a/b.csv"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Open(ctx, "a/b.csv"); err == nil {
		t.Fatal("file must be gone")
	}
	if err := l.Delete(ctx, "a/b.csv"); err != nil {
		t.Fatal("deleting a missing file is not an error")
	}
}
