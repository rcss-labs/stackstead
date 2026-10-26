// Package s3 is a small S3 client (SigV4, no SDK) covering what Stackstead
// backups need: single-shot put/get/delete, listing and streaming multipart
// upload. It works against AWS and S3-compatible stores (MinIO, B2, Wasabi).
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
)

// Config describes one bucket. Endpoint, when set, replaces the default
// https://s3.<region>.amazonaws.com base URL.
type Config struct {
	Bucket, Region, Endpoint string
	PathStyle                bool
	Creds                    awsauth.Credentials
}

// Object is one entry of a listing.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Client talks to one bucket.
type Client struct {
	cfg Config
	hc  *http.Client
	now func() time.Time
}

// New returns a client. hc and now may be nil.
func New(c Config, hc *http.Client, now func() time.Time) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &Client{cfg: c, hc: hc, now: now}
}

// Put stores body under key in a single request.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	resp, err := c.do(ctx, http.MethodPut, key, nil, body)
	if err != nil {
		return err
	}
	return closeBody(resp)
}

// Delete removes key.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		return err
	}
	return closeBody(resp)
}

// Get streams key. The caller closes the reader; the int64 is the object size.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// List returns every object whose key starts with prefix, following
// continuation tokens.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: decode listing: %w", err)
		}
		for _, o := range page.Contents {
			mod, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: o.Size, Modified: mod})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// do signs and sends one request, returning the response for 2xx statuses
// and an error carrying the S3 error Code and Message otherwise.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, body []byte) (*http.Response, error) {
	u, err := c.objectURL(key, query)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.ContentLength = int64(len(body))
	}
	if err := awsauth.SignV4(req, body, c.cfg.Creds, c.cfg.Region, "s3", c.now()); err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: %s %s: %w", method, key, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, parseError(method, key, resp)
	}
	return resp, nil
}

// objectURL places the bucket in the host (virtual-host style) or the path.
func (c *Client) objectURL(key string, query url.Values) (*url.URL, error) {
	scheme, host := "https", "s3."+c.cfg.Region+".amazonaws.com"
	if c.cfg.Endpoint != "" {
		e, err := url.Parse(c.cfg.Endpoint)
		if err != nil || e.Host == "" {
			return nil, fmt.Errorf("s3: invalid endpoint %q", c.cfg.Endpoint)
		}
		scheme, host = e.Scheme, e.Host
	}
	path := "/" + key
	if c.cfg.PathStyle {
		path = "/" + c.cfg.Bucket + path
	} else {
		host = c.cfg.Bucket + "." + host
	}
	u := &url.URL{Scheme: scheme, Host: host, Path: path, RawPath: encodePath(path)}
	if len(query) > 0 {
		u.RawQuery = encodeQuery(query)
	}
	return u, nil
}

// encodePath percent-encodes everything but unreserved characters and "/",
// matching how the request is signed.
func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = escape(s)
	}
	return strings.Join(segs, "/")
}

func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, escape(k)+"="+escape(v))
		}
	}
	return strings.Join(parts, "&")
}

func escape(s string) string {
	// url.PathEscape leaves sub-delims such as "+" and "=" alone, so escape
	// by hand to the RFC 3986 unreserved set.
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&15])
	}
	return b.String()
}

func parseError(method, key string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(raw, &e) == nil && e.Code != "" {
		return fmt.Errorf("s3: %s %s: %s: %s (HTTP %d)", method, key, e.Code, e.Message, resp.StatusCode)
	}
	return fmt.Errorf("s3: %s %s: HTTP %d", method, key, resp.StatusCode)
}

func closeBody(resp *http.Response) error {
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
