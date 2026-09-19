package s3store

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestFakePutListDelete(t *testing.T) {
	f := NewFake("https://media.test")
	url, err := f.Put(context.Background(), "audio/ep1.mp3", bytes.NewReader([]byte("data")), 4, "audio/mpeg")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if url != "https://media.test/audio/ep1.mp3" {
		t.Errorf("url = %q", url)
	}

	keys, err := f.ListKeys(context.Background(), "audio/")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "audio/ep1.mp3" {
		t.Errorf("keys = %v", keys)
	}

	// Other prefix excluded.
	if _, err := f.Put(context.Background(), "images/x.png", bytes.NewReader([]byte("x")), 1, "image/png"); err != nil {
		t.Fatalf("Put image: %v", err)
	}
	keys, _ = f.ListKeys(context.Background(), "audio/")
	if len(keys) != 1 {
		t.Errorf("prefix filter broken: %v", keys)
	}

	if err := f.Delete(context.Background(), "audio/ep1.mp3"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	keys, _ = f.ListKeys(context.Background(), "audio/")
	if len(keys) != 0 {
		t.Errorf("after delete keys = %v", keys)
	}

	// Body content is retained.
	if _, err := f.Put(context.Background(), "a.bin", bytes.NewReader([]byte("payload")), 7, "application/octet-stream"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rd, size, err := f.Get(context.Background(), "a.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rd.Close() }()
	b, _ := io.ReadAll(rd)
	if string(b) != "payload" || size != 7 {
		t.Errorf("got %q size=%d", b, size)
	}
}
