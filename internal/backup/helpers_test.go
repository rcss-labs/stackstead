package backup

import (
	"encoding/json"
	"io"
)

func decodeManifest(r io.Reader, m *Manifest) error { return json.NewDecoder(r).Decode(m) }
