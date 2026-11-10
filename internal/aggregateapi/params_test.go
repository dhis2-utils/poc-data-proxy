package aggregateapi

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"testing"
)

// A multipart body's large parts land in temporary files; params removes them (review
// R-18).
func TestParamsRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("de", "abc")
	fw, _ := mw.CreateFormFile("file", "big.bin")
	fw.Write(bytes.Repeat([]byte("x"), 2<<20))
	mw.Close()
	r := httptest.NewRequest("POST", "/api/dataEntry/dataValues", nil)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if q := params(r, buf.Bytes()); q.Get("de") != "abc" {
		t.Fatalf("params: %v", q)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
