package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
	"github.com/rcss-labs/stackstead/internal/s3"
)

type s3Dest struct {
	client *s3.Client
	prefix string

	mu      sync.Mutex
	uploads map[string]*s3.Upload
}

func newS3Client(cfg S3Dest, hc *http.Client) *s3.Client {
	return s3.New(s3.Config{
		Bucket:    cfg.Bucket,
		Region:    cfg.Region,
		Endpoint:  cfg.Endpoint,
		PathStyle: cfg.PathStyle,
		Creds:     awsauth.Credentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretKey},
	}, hc, nil)
}

// NewS3 returns a destination storing archives under cfg.Prefix in the bucket.
func NewS3(cfg S3Dest, hc *http.Client) Destination {
	return &s3Dest{
		client:  newS3Client(cfg, hc),
		prefix:  strings.Trim(cfg.Prefix, "/"),
		uploads: map[string]*s3.Upload{},
	}
}

func (d *s3Dest) Name() string { return destS3 }

func (d *s3Dest) key(name string) string { return path.Join(d.prefix, name) }

func (d *s3Dest) Create(ctx context.Context, archive string) (io.WriteCloser, error) {
	d.mu.Lock()
	_, busy := d.uploads[archive]
	d.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("upload of %s is already in progress", archive)
	}
	up, err := d.client.NewUpload(ctx, d.key(archive))
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if _, busy := d.uploads[archive]; busy {
		d.mu.Unlock()
		_ = up.Abort(ctx)
		return nil, fmt.Errorf("upload of %s is already in progress", archive)
	}
	d.uploads[archive] = up
	d.mu.Unlock()
	return &s3Writer{Upload: up, dest: d, archive: archive}, nil
}

// s3Writer forgets its upload once the upload has finished, successfully or not.
type s3Writer struct {
	*s3.Upload
	dest    *s3Dest
	archive string
}

func (w *s3Writer) Close() error {
	err := w.Upload.Close()
	w.dest.forget(w.archive)
	return err
}

func (d *s3Dest) forget(archive string) {
	d.mu.Lock()
	delete(d.uploads, archive)
	d.mu.Unlock()
}

func (d *s3Dest) Abort(ctx context.Context, archive string) error {
	d.mu.Lock()
	up := d.uploads[archive]
	delete(d.uploads, archive)
	d.mu.Unlock()
	if up == nil {
		return nil
	}
	return up.Abort(ctx)
}

func (d *s3Dest) PutSidecar(ctx context.Context, archive string, manifest []byte) error {
	return d.client.Put(ctx, d.key(SidecarName(archive)), manifest)
}

func (d *s3Dest) List(ctx context.Context) ([]ArchiveRef, error) {
	listPrefix := ""
	if d.prefix != "" {
		listPrefix = d.prefix + "/"
	}
	objects, err := d.client.List(ctx, listPrefix)
	if err != nil {
		return nil, err
	}
	var refs []ArchiveRef
	for _, o := range objects {
		name := strings.TrimPrefix(o.Key, listPrefix)
		slug, created, ok := ParseArchiveName(name)
		if !ok {
			continue
		}
		refs = append(refs, ArchiveRef{Dest: destS3, Name: name, HostSlug: slug, CreatedAt: created, Size: o.Size})
	}
	return refs, nil
}

func (d *s3Dest) Open(ctx context.Context, archive string) (io.ReadCloser, error) {
	rc, _, err := d.client.Get(ctx, d.key(archive))
	return rc, err
}

func (d *s3Dest) ReadSidecar(ctx context.Context, archive string) (Manifest, error) {
	rc, _, err := d.client.Get(ctx, d.key(SidecarName(archive)))
	if err != nil {
		return Manifest{}, err
	}
	defer rc.Close()
	var m Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest of %s: %w", archive, err)
	}
	return m, nil
}

// TestS3 checks the bucket settings by writing and deleting a throwaway object.
func TestS3(ctx context.Context, cfg S3Dest, hc *http.Client) error {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	client := newS3Client(cfg, hc)
	key := path.Join(strings.Trim(cfg.Prefix, "/"), ".stackstead-test-"+hex.EncodeToString(nonce[:]))
	if err := client.Put(ctx, key, []byte("stackstead connection test")); err != nil {
		return err
	}
	return client.Delete(ctx, key)
}
