package peers

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// IsPrivate reports whether addr is loopback, RFC 1918, RFC 4193 (fc00::/7)
// or link-local.
func IsPrivate(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// ValidateAddress parses a peer's base URL. https is the default; http is
// accepted only with plaintextAccepted and when every address the host
// resolves to is private.
func ValidateAddress(raw string, plaintextAccepted bool, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("the address is not a valid URL")
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, errors.New("the address scheme must be https (or http on a private network)")
	case u.Hostname() == "":
		return nil, errors.New("the address needs a host")
	case u.User != nil:
		return nil, errors.New("the address must not contain credentials")
	case u.RawQuery != "" || u.ForceQuery:
		return nil, errors.New("the address must not contain a query")
	case u.Fragment != "":
		return nil, errors.New("the address must not contain a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""

	if u.Scheme == "http" {
		if !plaintextAccepted {
			return nil, errors.New("plain http needs the plaintext acceptance and a private-network address")
		}
		if err := requirePrivateHost(u.Hostname(), resolve); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func requirePrivateHost(host string, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) error {
	addrs, err := literalOrResolve(host, resolve)
	if err != nil {
		return fmt.Errorf("could not resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%s did not resolve to any address", host)
	}
	for _, a := range addrs {
		if !IsPrivate(a) {
			return fmt.Errorf("plain http is only allowed for private-network addresses, but %s resolves to a non-private address", host)
		}
	}
	return nil
}

func literalOrResolve(host string, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if resolve == nil {
		return nil, errors.New("no resolver")
	}
	return resolve(context.Background(), host)
}
