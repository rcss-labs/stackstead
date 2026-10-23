// Package secret provides a string type that is redacted in every formatting,
// logging and JSON path except an explicit Reveal call.
package secret

import "log/slog"

const redacted = "[redacted]"

// Secret is a credential that must not leak into logs or API output.
type Secret string

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// Reveal returns the plaintext; use it only where the credential is needed.
func (s Secret) Reveal() string { return string(s) }
