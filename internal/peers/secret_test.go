package peers

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	s := Secret("ssp_supersecret")
	out := fmt.Sprintf("%v %s %+v %#v", s, s, struct{ S Secret }{s}, s)
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "cred", s)
	for _, got := range []string{out, buf.String()} {
		if strings.Contains(got, "ssp_") {
			t.Errorf("secret leaked: %s", got)
		}
	}
	if s.Reveal() != "ssp_supersecret" {
		t.Error("Reveal must return the plaintext")
	}
}

func TestNewCredential(t *testing.T) {
	s, hash, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Reveal(), "ssp_") || hash != HashSecret(s.Reveal()) {
		t.Errorf("bad credential or hash: %q", hash)
	}
	other, _, _ := NewCredential()
	if other == s {
		t.Error("credentials must be random")
	}
}
