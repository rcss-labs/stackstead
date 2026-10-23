package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/peers"
)

// peerTarWriteTimeout bounds every write of an image stream to a peer.
const peerTarWriteTimeout = 10 * time.Second

// registerPeerImageRoutes adds the read-only image routes a paired server may
// call. They need a peer credential; operator routes live under /api/images.
func (s *server) registerPeerImageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peer/images", s.requirePeer(false, s.handlePeerImages))
	mux.HandleFunc("GET /api/peer/images/usage", s.requirePeer(false, s.handlePeerImageUsage))
	mux.HandleFunc("GET /api/peer/images/{slug}/versions/{tag}/tar", s.requirePeer(false, s.handlePeerImageTar))
}

func (s *server) handlePeerImages(w http.ResponseWriter, r *http.Request, _ peers.Peer) {
	entries, err := s.opts.Images.OwnImages(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	// Versions carry no logs and no push state; only succeeded versions exist here.
	out := make([]apitypes.LibraryImageDetail, 0, len(entries))
	for _, e := range entries {
		base, versions := toLibraryImageBase(e.Image, e.Owner, e.Versions, nil, nil)
		out = append(out, apitypes.LibraryImageDetail{LibraryImage: base, Versions: versions})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handlePeerImageUsage(w http.ResponseWriter, r *http.Request, _ peers.Peer) {
	usages, err := s.opts.Images.OwnUsage(r.Context())
	if err != nil {
		s.writeImagesError(w, r, err)
		return
	}
	out := make([]apitypes.LibraryUsage, 0, len(usages))
	for _, u := range usages {
		out = append(out, apitypes.LibraryUsage{Ref: u.Ref.String(), Stack: u.Stack, Service: u.Service})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handlePeerImageTar(w http.ResponseWriter, r *http.Request, _ peers.Peer) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.opts.Closing: // shutdown must not wait on a long stream
			cancel()
		case <-ctx.Done():
		}
	}()
	rc, imageID, err := s.opts.Images.OpenVersionTar(ctx, r.PathValue("slug"), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, images.ErrImageNotFound) || errors.Is(err, images.ErrVersionNotFound) || errors.Is(err, images.ErrImageNotBuilt) {
			writeError(w, http.StatusNotFound, "image version not found")
			return
		}
		s.writeImagesError(w, r, err)
		return
	}
	defer rc.Close()
	// Closing the engine stream unblocks a read that would otherwise outlive the request.
	stop := context.AfterFunc(ctx, func() { rc.Close() })
	defer stop()

	h := w.Header()
	h.Set("Content-Type", "application/x-tar")
	h.Set("X-Stackstead-Image-ID", imageID)
	h.Set("Cache-Control", "no-store")
	dw := &deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: peerTarWriteTimeout}
	flush := http.NewResponseController(dw)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if _, werr := dw.Write(buf[:n]); werr != nil {
				return
			}
			_ = flush.Flush()
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				// Headers are out: cut the stream so the peer sees a failed transfer.
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}
