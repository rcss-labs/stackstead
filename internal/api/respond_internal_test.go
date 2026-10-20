package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONLimit(t *testing.T) {
	body := `{"content":"` + strings.Repeat("a", 2<<20) + `"}`
	decode := func(limit int64) bool {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var dst struct{ Content string }
		return decodeJSONLimit(httptest.NewRecorder(), r, &dst, limit)
	}
	if decode(maxBodyBytes) {
		t.Error("2 MiB body accepted under the default cap")
	}
	if !decode(dockerfileBodyBytes) {
		t.Error("2 MiB body refused under the Dockerfile cap")
	}
	if createBodyBytes != 32<<20 || dockerfileBodyBytes != 8<<20 {
		t.Error("body caps changed")
	}
}
