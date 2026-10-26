// Package awsauth hand-rolls the AWS authentication pieces Stackstead needs
// without an SDK: Signature Version 4, IMDSv2 role credentials and static-key
// profiles from the shared credentials/config files.
package awsauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// Credentials are AWS access keys. The secret key and session token are
// secret.Secret so they redact in logs and JSON.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey secret.Secret
	SessionToken    secret.Secret
}

const (
	algorithm  = "AWS4-HMAC-SHA256"
	timeFormat = "20060102T150405Z"
)

// SignV4 signs req in place: it sets X-Amz-Date, X-Amz-Security-Token (when
// the credentials carry a session token) and Authorization. The host comes
// from req.URL. All request headers present at call time are signed.
func SignV4(req *http.Request, body []byte, c Credentials, region, service string, now time.Time) error {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return errors.New("awsauth: access key id and secret access key are required")
	}
	if region == "" || service == "" {
		return errors.New("awsauth: region and service are required")
	}
	now = now.UTC()
	amzDate := now.Format(timeFormat)
	day := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if service == "s3" {
		// S3 requires the payload hash as a signed header.
		req.Header.Set("X-Amz-Content-Sha256", hexSHA256(body))
	}
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken.Reveal())
	} else {
		req.Header.Del("X-Amz-Security-Token")
	}
	req.Header.Del("Authorization")

	host := req.URL.Host
	if req.Host != "" {
		host = req.Host
	}
	// Header names that differ only in case merge into one entry; a Host
	// entry in req.Header is ignored in favour of the real request host.
	rawNames := make([]string, 0, len(req.Header))
	for name := range req.Header {
		rawNames = append(rawNames, name)
	}
	sort.Strings(rawNames)
	values := map[string][]string{}
	for _, name := range rawNames {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "host" {
			continue
		}
		for _, v := range req.Header[name] {
			values[lower] = append(values[lower], strings.Join(strings.Fields(v), " "))
		}
	}
	headers := map[string]string{"host": host}
	for name, vals := range values {
		headers[name] = strings.Join(vals, ",")
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + headers[n] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL, service == "s3"),
		canonicalQuery(req.URL),
		canonHeaders.String(),
		signedHeaders,
		hexSHA256(body),
	}, "\n")

	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := algorithm + "\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canonical))

	key := hmacSHA256([]byte("AWS4"+c.SecretAccessKey.Reveal()), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, c.AccessKeyID, scope, signedHeaders, signature))
	return nil
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalPath double-encodes the path as required for every service except
// S3, which encodes each decoded segment exactly once.
func canonicalPath(u *url.URL, s3 bool) string {
	if s3 {
		if u.Path == "" {
			return "/"
		}
		segs := strings.Split(u.Path, "/")
		for i, s := range segs {
			segs[i] = rfc3986(s)
		}
		return strings.Join(segs, "/")
	}
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = rfc3986(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		dk, err1 := url.QueryUnescape(k)
		dv, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			dk, dv = k, v
		}
		pairs = append(pairs, kv{rfc3986(dk), rfc3986(dv)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// rfc3986 percent-encodes everything except unreserved characters.
func rfc3986(s string) string {
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
