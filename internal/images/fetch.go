package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const (
	// fetchIdleTimeout cancels a transfer when no Read of the body completes for this long.
	fetchIdleTimeout = 60 * time.Second
	// fetchProgressEvery is how often a running fetch reports its size.
	fetchProgressEvery = 5 * time.Second
	// fetchHeaderTimeout bounds the wait for the owner's response headers.
	fetchHeaderTimeout = 30 * time.Second

	imageIDHeader  = "X-Stackstead-Image-ID"
	peerUserHeader = "X-Stackstead-Peer-User"
	fetchActor     = "system"
)

var (
	// ErrArchMismatch is matched by *ArchMismatchError.
	ErrArchMismatch = errors.New("image architecture does not match this server")
	// ErrFetchRunning means a fetch of this image is already in progress.
	ErrFetchRunning = errors.New("a fetch is already running for this image")

	errStalled = errors.New("transfer stalled")

	mismatchMessage = "fetched image does not match the owner's record"
)

// ArchMismatchError says the owner's version was built for another architecture.
type ArchMismatchError struct{ Built, Local string }

func (e *ArchMismatchError) Error() string {
	return "built for " + e.Built + "; this server is " + e.Local
}

func (e *ArchMismatchError) Is(target error) bool { return target == ErrArchMismatch }

// peerVersion finds the owner of ref and its recorded succeeded version. It
// uses the cached peer library and never contacts a stranger.
func (s *Service) peerVersion(ctx context.Context, ref imageref.Ref) (ServerInfo, Version, error) {
	unknown := fieldError("ref", "this image belongs to a server that is not connected")
	owner, ok, err := s.FindOwner(ctx, ref.Owner)
	if err != nil {
		return ServerInfo{}, Version{}, err
	}
	if !ok {
		return ServerInfo{}, Version{}, unknown
	}
	results, err := s.peerLibraries(ctx)
	if err != nil {
		return ServerInfo{}, Version{}, err
	}
	for _, res := range results {
		if res.server.ID != owner.ID {
			continue
		}
		switch res.server.Status {
		case statusOld:
			return ServerInfo{}, Version{}, &peers.PeerError{Code: peers.CodeOld, Server: owner.Name, Msg: res.server.Error}
		case statusUnreachable:
			msg := res.server.Error
			if msg == "" {
				msg = owner.Name + " could not be reached"
			}
			return ServerInfo{}, Version{}, &peers.PeerError{Code: peers.CodeUnreachable, Server: owner.Name, Msg: msg}
		}
		for _, e := range res.images {
			if e.Slug != ref.Slug {
				continue
			}
			for _, v := range e.Versions {
				if v.Tag == ref.Tag && v.Status == VersionSucceeded {
					return res.server, v, nil
				}
			}
		}
		return ServerInfo{}, Version{}, &NotBuiltError{Slug: ref.Slug, Tag: ref.Tag}
	}
	return ServerInfo{}, Version{}, unknown
}

// presentWithID reports whether ref is in the local engine with exactly id.
func (s *Service) presentWithID(ctx context.Context, ref imageref.Ref, id string) (bool, error) {
	in, err := s.eng.InspectImage(ctx, ref.String())
	if errors.Is(err, engine.ErrImageNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", ref, err)
	}
	return id != "" && in.ID == id, nil
}

// checkArch refuses a version built for another architecture before any transfer.
func (s *Service) checkArch(ctx context.Context, v Version) error {
	local, err := s.eng.Arch(ctx)
	if err != nil {
		return fmt.Errorf("read engine architecture: %w", err)
	}
	if v.Arch == "" {
		return &peers.PeerError{Code: peers.CodeVersion, Msg: "the owner did not record an architecture"}
	}
	if v.Arch != local {
		return &ArchMismatchError{Built: v.Arch, Local: local}
	}
	return nil
}

// Fetch copies a peer-owned version into the local engine as an operation.
// started is false when there is nothing to do: the ref is owned by this
// server, or the image is already here with the owner's recorded ID.
func (s *Service) Fetch(ctx context.Context, user string, ref imageref.Ref) (op stacks.Operation, started bool, err error) {
	_, self, err := s.selfInfo(ctx)
	if err != nil {
		return stacks.Operation{}, false, err
	}
	if ref.Owner == self {
		return stacks.Operation{}, false, nil
	}
	owner, v, err := s.peerVersion(ctx, ref)
	if err != nil {
		return stacks.Operation{}, false, err
	}
	if present, err := s.presentWithID(ctx, ref, v.ImageDigest); err != nil || present {
		return stacks.Operation{}, false, err
	}
	if err := s.checkArch(ctx, v); err != nil {
		return stacks.Operation{}, false, err
	}
	op, err = s.ops.StartTimeout(OpKey(ref.Owner, ref.Slug), "fetch", s.timeout, func(opCtx context.Context, out func(string)) error {
		return s.fetchShared(opCtx, user, ref, owner, v, out)
	})
	if errors.Is(err, stacks.ErrBusy) {
		return stacks.Operation{}, false, ErrFetchRunning
	}
	if err != nil {
		return stacks.Operation{}, false, err
	}
	return op, true, nil
}

// fetchFrom transfers ref from owner, verifies it and records the outcome.
func (s *Service) fetchFrom(ctx context.Context, user string, ref imageref.Ref, owner ServerInfo, v Version, out func(string)) error {
	if out == nil {
		out = func(string) {}
	}
	var received atomic.Int64
	err := s.transfer(ctx, user, ref, owner, v, &received, out)
	bg := context.WithoutCancel(ctx)
	detail := fmt.Sprintf("%s %d", owner.Name, received.Load())
	if err != nil {
		msg := err.Error()
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			err = errors.New("fetch timed out")
		case ctx.Err() != nil:
			err = errors.New("fetch cancelled")
		}
		if msg = err.Error(); msg != "" {
			out("fetch failed: " + msg)
		}
		s.record(bg, user, "image.fetch", ref.Slug, ref.Tag, detail+": "+msg, "failed", "")
		return err
	}
	s.record(bg, user, "image.fetch", ref.Slug, ref.Tag, detail, "succeeded", "")
	return nil
}

func (s *Service) transfer(ctx context.Context, user string, ref imageref.Ref, owner ServerInfo, v Version, received *atomic.Int64, out func(string)) error {
	p, err := s.peers.Usable(owner.ID)
	if err != nil {
		return err
	}
	unreachable := &peers.PeerError{Code: peers.CodeUnreachable, Server: p.Name, Msg: p.Name + " could not be reached"}
	base, err := url.Parse(p.URL)
	if err != nil {
		return unreachable
	}
	client, err := peers.NewHTTPClient(base, p.TLS, fetchHeaderTimeout)
	if err != nil {
		return unreachable
	}
	defer client.CloseIdleConnections()

	fctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	target := p.URL + "/api/peer/images/" + url.PathEscape(ref.Slug) + "/versions/" + url.PathEscape(ref.Tag) + "/tar"
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, target, nil)
	if err != nil {
		return unreachable
	}
	req.Header.Set(peers.PeerHeader, p.OutCredential.Reveal())
	if user != "" {
		req.Header.Set(peerUserHeader, user)
	}
	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return s.peerFailureFor(ref, peers.Classify(p.Name, resp, err), p.Name)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/x-tar" {
		return &peers.PeerError{Code: peers.CodeProxy, Server: p.Name, Msg: p.Name + " did not send an image"}
	}
	wantID := resp.Header.Get(imageIDHeader)
	if wantID == "" || wantID != v.ImageDigest {
		return errors.New(mismatchMessage)
	}

	timer := time.AfterFunc(s.fetchIdle, func() { cancel(errStalled) })
	defer timer.Stop()
	insp := newTarInspector(&idleReader{r: resp.Body, timer: timer, idle: s.fetchIdle, n: received}, ref.String())
	defer insp.close()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(s.fetchProgress)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				out(fmt.Sprintf("fetching %s from %s (%s)", ref, owner.Name, humanBytes(received.Load())))
			case <-done:
				return
			}
		}
	}()
	loadErr := s.eng.LoadImage(fctx, insp)
	if verr := insp.violation(); verr != nil {
		cancel(verr) // a consumer that ignored the read error must not finish the load
	}
	close(done)
	wg.Wait()
	if verr := insp.violation(); verr != nil {
		return verr
	}
	if loadErr != nil {
		if errors.Is(context.Cause(fctx), errStalled) {
			return fmt.Errorf("transfer from %s stalled", owner.Name)
		}
		return fmt.Errorf("load %s: %w", ref, loadErr)
	}

	in, err := s.eng.InspectImage(ctx, ref.String())
	if err != nil {
		return fmt.Errorf("inspect fetched image: %w", err)
	}
	if in.ID != wantID {
		if _, rerr := s.eng.RemoveImage(context.WithoutCancel(ctx), ref.String()); rerr != nil {
			s.log.Warn("remove mismatched fetched image failed", "ref", ref.String(), "err", rerr)
		}
		return errors.New(mismatchMessage)
	}
	out("loaded " + ref.String())
	return nil
}

// peerFailureFor turns a classified failure of the tar request into the
// error callers see. Nothing here carries the URL or credential.
func (s *Service) peerFailureFor(ref imageref.Ref, err error, server string) error {
	if err == nil {
		return &peers.PeerError{Code: peers.CodeProxy, Server: server, Msg: server + " did not send an image"}
	}
	var re *peers.RemoteError
	if errors.As(err, &re) {
		if re.Status == http.StatusNotFound {
			return &NotBuiltError{Slug: ref.Slug, Tag: ref.Tag}
		}
		return &peers.PeerError{Code: peers.CodeUnreachable, Server: server, Msg: err.Error()}
	}
	return err
}

// idleReader counts bytes and re-arms the idle timer after every completed Read.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
	n     *atomic.Int64
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	i.n.Add(int64(n))
	if err != nil {
		i.timer.Stop() // the watchdog covers the transfer only, not what docker does after EOF
	} else {
		i.timer.Reset(i.idle)
	}
	return n, err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// EnsureLocal makes ref present in the local engine for a stack about to run.
// A fetch here runs inline in the caller's operation (a stack key), so it
// never needs a second slot in the operations manager.
func (s *Service) EnsureLocal(ctx context.Context, ref imageref.Ref, out func(string)) error {
	_, self, err := s.selfInfo(ctx)
	if err != nil {
		return err
	}
	if ref.Owner == self {
		return s.ensureOwn(ctx, ref)
	}
	owner, v, lookupErr := s.peerVersion(ctx, ref)
	if lookupErr != nil {
		// The owner may be offline while the image is already here.
		if _, err := s.eng.InspectImage(ctx, ref.String()); err == nil {
			return nil
		}
		var unknownOwner *ValidationError
		if errors.As(lookupErr, &unknownOwner) { // a server that is not connected: the image cannot come from anywhere
			return &NotBuiltError{Slug: ref.Slug, Tag: ref.Tag}
		}
		return lookupErr
	}
	if present, err := s.presentWithID(ctx, ref, v.ImageDigest); err != nil || present {
		return err
	}
	if err := s.checkArch(ctx, v); err != nil {
		return err
	}
	return s.fetchShared(ctx, fetchActor, ref, owner, v, out)
}

// fetchFlight is one transfer shared by every caller that wants the same ref.
type fetchFlight struct {
	done chan struct{}
	err  error
}

// fetchShared runs one transfer per ref at a time. A concurrent caller waits
// for it and shares its outcome, so a failed verification's untag can never
// remove an image a sibling transfer has just loaded.
func (s *Service) fetchShared(ctx context.Context, user string, ref imageref.Ref, owner ServerInfo, v Version, out func(string)) error {
	key := ref.String()
	s.fetchMu.Lock()
	if f, ok := s.fetches[key]; ok {
		s.fetchMu.Unlock()
		if out != nil {
			out("waiting for the transfer of " + key + " already in progress")
		}
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f := &fetchFlight{done: make(chan struct{})}
	if s.fetches == nil {
		s.fetches = map[string]*fetchFlight{}
	}
	s.fetches[key] = f
	s.fetchMu.Unlock()
	defer func() {
		s.fetchMu.Lock()
		delete(s.fetches, key)
		s.fetchMu.Unlock()
		close(f.done)
	}()
	// A sibling may have finished between the caller's check and this flight.
	if present, err := s.presentWithID(ctx, ref, v.ImageDigest); err != nil || present {
		f.err = err
		return err
	}
	f.err = s.fetchFrom(ctx, user, ref, owner, v, out)
	return f.err
}
