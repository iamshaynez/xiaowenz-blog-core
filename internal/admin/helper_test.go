package admin

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"testing"
)

// makeMultipart builds a one-file multipart body and its Content-Type.
func makeMultipart(t *testing.T, field, filename, contentType string, content []byte) (*bytes.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	h.Set("Content-Type", contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return bytes.NewReader(buf.Bytes()), w.FormDataContentType()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
