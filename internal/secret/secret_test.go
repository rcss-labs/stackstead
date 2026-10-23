package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretRedacts(t *testing.T) {
	s := Secret("ssp_x")
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "cred", s)
	out, err := json.Marshal(struct{ S Secret }{s})
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []string{
		fmt.Sprint(s),
		fmt.Sprintf("%#v", s),
		fmt.Sprintf("%+v", struct{ S Secret }{s}),
		buf.String(),
		string(out),
	} {
		if strings.Contains(got, "ssp_x") {
			t.Errorf("secret leaked: %s", got)
		}
	}
	if !strings.Contains(string(out), "[redacted]") {
		t.Errorf("json not redacted: %s", out)
	}
	if s.Reveal() != "ssp_x" {
		t.Error("Reveal must return the plaintext")
	}
}
