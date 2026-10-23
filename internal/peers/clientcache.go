package peers

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientCache keeps one HTTP client (and so one transport) per peer and
// header timeout, so connections are reused across calls. An entry is
// replaced, and its idle connections closed, when the peer's address or trust
// settings change. The zero value is ready to use.
type ClientCache struct {
	mu      sync.Mutex
	entries map[clientKey]clientEntry
}

type clientKey struct {
	id      string
	timeout time.Duration
}

type clientEntry struct {
	settings string
	client   *http.Client
}

// Get returns the cached client for p, building one when there is none or
// p's URL or trust settings differ from the cached one's. Credentials are
// sent per request, so they do not need to invalidate a client.
func (c *ClientCache) Get(p Peer, base *url.URL, timeout time.Duration) (*http.Client, error) {
	settings := strings.Join([]string{p.URL, p.TLS.CAPEM, p.TLS.PinSHA256, boolString(p.TLS.Plaintext)}, "\x00")
	key := clientKey{p.InstanceID, timeout}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.settings == settings {
		return e.client, nil
	}
	client, err := NewHTTPClient(base, p.TLS, timeout)
	if err != nil {
		return nil, err
	}
	if old, ok := c.entries[key]; ok {
		old.client.CloseIdleConnections()
	}
	if c.entries == nil {
		c.entries = map[clientKey]clientEntry{}
	}
	c.entries[key] = clientEntry{settings: settings, client: client}
	return client, nil
}

// Forget drops and closes the clients of a peer, for when it is unpaired.
func (c *ClientCache) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.entries {
		if key.id == id {
			e.client.CloseIdleConnections()
			delete(c.entries, key)
		}
	}
}

func boolString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
