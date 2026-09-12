package storage

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestLocalFSRoundTrip(t *testing.T) {
	st, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	obj, err := st.Put(ctx, "application-avatars/1/test.png", bytes.NewReader([]byte("hello-bytes")), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size != 11 {
		t.Fatalf("size = %d", obj.Size)
	}

	rc, got, err := st.Open(ctx, "application-avatars/1/test.png")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close() // Windows: close before delete
	if string(data) != "hello-bytes" {
		t.Fatalf("content = %q", data)
	}
	if got.Key != "application-avatars/1/test.png" {
		t.Fatalf("key = %q", got.Key)
	}

	if err := st.Delete(ctx, "application-avatars/1/test.png"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Open(ctx, "application-avatars/1/test.png"); err != ErrNotFound {
		t.Fatalf("deleted object: err = %v, want ErrNotFound", err)
	}
}

func TestSanitizeKeyRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "a/../../b", "..\\..\\x", ""} {
		if _, err := SanitizeKey(bad); err == nil {
			t.Fatalf("traversal key %q accepted", bad)
		}
	}
	ok, err := SanitizeKey("application-avatars/2026/09/a.png")
	if err != nil || ok != "application-avatars/2026/09/a.png" {
		t.Fatalf("clean key = %q, err = %v", ok, err)
	}
}
