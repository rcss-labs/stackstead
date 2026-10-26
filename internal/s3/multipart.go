package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// PartSize is the multipart part size (S3's minimum for non-final parts is 5 MiB).
const PartSize = 16 << 20

// Upload streams data to a key as a multipart upload. It buffers one part in
// a single reused slice, so memory use is PartSize regardless of object size.
type Upload struct {
	c        *Client
	ctx      context.Context
	key      string
	uploadID string
	buf      []byte
	etags    []string
	err      error
	closed   bool
}

var _ io.WriteCloser = (*Upload)(nil)

// NewUpload starts a multipart upload for key.
func (c *Client) NewUpload(ctx context.Context, key string) (*Upload, error) {
	resp, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil || out.UploadID == "" {
		return nil, fmt.Errorf("s3: create multipart upload %s: no upload id in response", key)
	}
	return &Upload{c: c, ctx: ctx, key: key, uploadID: out.UploadID, buf: make([]byte, 0, PartSize)}, nil
}

// Write buffers p and uploads a part each time 16 MiB has accumulated.
func (u *Upload) Write(p []byte) (int, error) {
	if u.err != nil {
		return 0, u.err
	}
	if u.closed {
		return 0, errors.New("s3: write to closed upload")
	}
	written := 0
	for len(p) > 0 {
		room := PartSize - len(u.buf)
		n := min(room, len(p))
		u.buf = append(u.buf, p[:n]...)
		p = p[n:]
		written += n
		if len(u.buf) == PartSize {
			if err := u.flush(); err != nil {
				u.err = err
				return written, err
			}
		}
	}
	return written, nil
}

// Close uploads the final part and completes the upload. On any failure it
// aborts the upload so no orphaned parts are left billing.
func (u *Upload) Close() error {
	if u.closed {
		return u.err
	}
	u.closed = true
	if u.err == nil && (len(u.buf) > 0 || len(u.etags) == 0) {
		u.err = u.flush()
	}
	if u.err == nil {
		u.err = u.complete()
	}
	if u.err != nil {
		// Abort must outlive a cancelled request context.
		_ = u.Abort(context.WithoutCancel(u.ctx))
	}
	return u.err
}

// Abort cancels the multipart upload and discards uploaded parts.
func (u *Upload) Abort(ctx context.Context) error {
	resp, err := u.c.do(ctx, http.MethodDelete, u.key, url.Values{"uploadId": {u.uploadID}}, nil)
	if err != nil {
		return err
	}
	return closeBody(resp)
}

func (u *Upload) flush() error {
	part := len(u.etags) + 1
	q := url.Values{"partNumber": {strconv.Itoa(part)}, "uploadId": {u.uploadID}}
	resp, err := u.c.do(u.ctx, http.MethodPut, u.key, q, u.buf)
	if err != nil {
		return err
	}
	etag := resp.Header.Get("ETag")
	if err := closeBody(resp); err != nil {
		return err
	}
	if etag == "" {
		return fmt.Errorf("s3: upload part %d of %s: response had no ETag", part, u.key)
	}
	u.etags = append(u.etags, etag)
	u.buf = u.buf[:0]
	return nil
}

func (u *Upload) complete() error {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i, etag := range u.etags {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>", i+1)
		_ = xml.EscapeText(&b, []byte(etag))
		b.WriteString("</ETag></Part>")
	}
	b.WriteString("</CompleteMultipartUpload>")
	resp, err := u.c.do(u.ctx, http.MethodPost, u.key, url.Values{"uploadId": {u.uploadID}}, []byte(b.String()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// S3 may answer 200 and still carry an <Error> document in the body.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(raw, &e) == nil && e.Code != "" {
		return fmt.Errorf("s3: complete multipart upload %s: %s: %s", u.key, e.Code, e.Message)
	}
	return nil
}
