package updatehost

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

type executableInspection struct {
	Identity update.Identity
	Slices   []update.Identity
}
type payloadEntry struct {
	Path      string
	Size      int64
	Directory bool
	Metadata  *payloadMetadataProof
}
type payloadInspection struct {
	Artifact                             update.VerifiedArtifact
	Installer                            update.InstallerKind
	Identifier, Version, InstallLocation string
	Entries                              []payloadEntry
	App, Helper, Bootstrap               executableInspection
	DecodedBytes                         int64
}
type payloadRequest struct {
	Artifact  update.VerifiedArtifact
	Installer update.InstallerKind
	Identity  update.Identity
}
type payloadBounds struct {
	Decoded, File  int64
	Entries, Names int
}

var defaultPayloadBounds = payloadBounds{2 << 30, 512 << 20, 4096, 64 << 10}

const payloadTimeout = 120 * time.Second

func (e *childExecutor) inspectPayload(ctx context.Context, jobID string, p update.Plan, a update.VerifiedArtifact) (payloadInspection, error) {
	expected, err := nativeArtifactIdentity(p, a)
	if err != nil {
		return payloadInspection{}, err
	}
	if len(p.Registration.Instances) == 0 {
		return payloadInspection{}, update.ErrSetupRequired
	}
	// Only the parent opens its private artifact; the child inherits the verified handle.
	dir, err := trustedDirectory(path.Dir(a.Path), e.owner, e.anchor)
	if err != nil {
		return payloadInspection{}, err
	}
	defer dir.Close()
	f, err := openPayloadArtifact(dir, path.Base(a.Path), e.owner)
	if err != nil {
		return payloadInspection{}, err
	}
	defer f.Close()
	if err = verifyPayloadInput(a, f); err != nil {
		return payloadInspection{}, err
	}
	in := p.Registration.Instances[0]
	in.ConfigPath = ""
	in.DataDir = ""
	in.ControlSocket = ""
	r, err := e.execute(ctx, childRequest{Protocol: 1, Role: childInspectPayload, JobID: jobID, Instance: in, Limit: a.Size, Payload: payloadRequest{a, p.Registration.Installer, expected}}, f, nil, nil)
	if err != nil {
		return payloadInspection{}, err
	}
	if r.Payload == nil || r.Inspection != nil || r.Snapshot != nil {
		return payloadInspection{}, update.ErrIncompatible
	}
	proof := *r.Payload
	if proof.Artifact != a || proof.Installer != p.Registration.Installer || validatePayloadInventory(proof) != nil || validatePayloadIdentities(proof, expected, p.Registration.Installer == update.InstallerPkg) != nil {
		return payloadInspection{}, update.ErrIncompatible
	}
	if err = verifyPayloadInput(a, f); err != nil {
		return payloadInspection{}, err
	}
	return proof, nil
}
func verifyPayloadInput(a update.VerifiedArtifact, f *os.File) error {
	if f == nil || a.Size <= 0 || a.Size > childArtifactLimit || !childDigest.MatchString(a.SHA256) {
		return update.ErrIncompatible
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != a.Size {
		return update.ErrIncompatible
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, a.Size+1))
	if err != nil || n != a.Size || fmt.Sprintf("%x", h.Sum(nil)) != a.SHA256 {
		return update.ErrIncompatible
	}
	_, err = f.Seek(0, 0)
	return err
}
func inspectChildPayload(ctx context.Context, req payloadRequest, input *os.File) (payloadInspection, error) {
	ctx, cancel := context.WithTimeout(ctx, payloadTimeout)
	defer cancel()
	if err := verifyPayloadInput(req.Artifact, input); err != nil {
		return payloadInspection{}, err
	}
	var proof payloadInspection
	var err error
	switch req.Installer {
	case update.InstallerScript:
		var gz *gzip.Reader
		gz, err = gzip.NewReader(input)
		if err == nil {
			defer gz.Close()
			proof, err = inventoryTar(ctx, gz, req, defaultPayloadBounds, true)
		}
	default:
		proof, err = inspectNativePayload(ctx, req, input)
	}
	if err != nil {
		return payloadInspection{}, err
	}
	proof.Artifact = req.Artifact
	proof.Installer = req.Installer
	if err = validatePayloadIdentities(proof, req.Identity, req.Installer == update.InstallerPkg); err != nil {
		return payloadInspection{}, err
	}
	return proof, nil
}
func validatePayloadIdentities(p payloadInspection, want update.Identity, universal bool) error {
	for _, b := range []executableInspection{p.App, p.Helper, p.Bootstrap} {
		if b.Identity != want {
			return update.ErrIncompatible
		}
		if universal {
			if len(b.Slices) != 2 {
				return update.ErrIncompatible
			}
			for i, a := range []string{"amd64", "arm64"} {
				id := want
				id.Arch = a
				if b.Slices[i] != id {
					return update.ErrIncompatible
				}
			}
		} else {
			if len(b.Slices) != 1 || b.Slices[0] != want {
				return update.ErrIncompatible
			}
		}
	}
	return nil
}
func inventoryTar(ctx context.Context, source io.Reader, req payloadRequest, bounds payloadBounds, identities bool) (payloadInspection, error) {
	if bounds.Decoded <= 0 || bounds.Decoded > 2<<30 || bounds.File <= 0 || bounds.File > childArtifactLimit || bounds.Entries <= 0 || bounds.Entries > 4096 || bounds.Names <= 0 || bounds.Names > 64<<10 {
		return payloadInspection{}, update.ErrIncompatible
	}
	lr := &io.LimitedReader{R: source, N: bounds.Decoded + 1}
	tr := tar.NewReader(lr)
	proof := payloadInspection{}
	seen := map[string]bool{}
	names := 0
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return proof, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return proof, err
		}
		name, err := payloadName(h.Name)
		if err != nil {
			return proof, err
		}
		entries++
		if entries > bounds.Entries {
			return proof, update.ErrIncompatible
		}
		if name == "/" && h.Typeflag == tar.TypeDir {
			if seen[name] || h.Size != 0 || h.Linkname != "" || len(h.PAXRecords) != 0 || h.Format == tar.FormatPAX || h.Format == tar.FormatGNU {
				return proof, update.ErrIncompatible
			}
			seen[name] = true
			continue
		}
		if len(h.PAXRecords) != 0 || h.Format == tar.FormatPAX || h.Format == tar.FormatGNU || h.Linkname != "" || h.Size < 0 || h.Size > bounds.File || seen[name] || len(proof.Entries) >= bounds.Entries || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir) {
			return proof, update.ErrIncompatible
		}
		if h.Typeflag == tar.TypeDir && h.Size != 0 {
			return proof, update.ErrIncompatible
		}
		names += len(name)
		if names > bounds.Names {
			return proof, update.ErrIncompatible
		}
		seen[name] = true
		proof.Entries = append(proof.Entries, payloadEntry{Path: name, Size: h.Size, Directory: h.Typeflag == tar.TypeDir})
		if companion, ok := metadataCompanion(name); ok && req.Installer == update.InstallerPkg {
			if h.Typeflag == tar.TypeDir || h.Size > 4096 {
				return proof, update.ErrIncompatible
			}
			b, e := io.ReadAll(io.LimitReader(tr, h.Size))
			if e != nil {
				return proof, e
			}
			if e = validateProvenanceMetadata(b); e != nil {
				return proof, e
			}
			proof.Entries[len(proof.Entries)-1].Metadata = &payloadMetadataProof{Path: name, Companion: companion, Attribute: "com.apple.provenance", Size: h.Size}
			continue
		}
		role := payloadBinaryRole(name, req.Installer)
		if identities && role != "" {
			if h.Mode&0111 == 0 {
				return proof, update.ErrIncompatible
			}
			f, err := os.CreateTemp("", "stackstead-payload-")
			if err != nil {
				return proof, err
			}
			func() {
				defer os.Remove(f.Name())
				defer f.Close()
				if _, err = io.CopyN(f, tr, h.Size); err != nil {
					return
				}
				var before unix.Stat_t
				if err = unix.Fstat(int(f.Fd()), &before); err != nil {
					return
				}
				if err = f.Sync(); err != nil {
					return
				}
				if err = f.Close(); err != nil {
					return
				}
				// Linux refuses exec while any staging writer remains open. Reopen the
				// same inode read-only, without following a substituted path.
				var fd int
				fd, err = unix.Open(f.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
				if err != nil {
					return
				}
				readOnly := os.NewFile(uintptr(fd), f.Name())
				defer readOnly.Close()
				var after unix.Stat_t
				if unix.Fstat(fd, &after) != nil || after.Dev != before.Dev || after.Ino != before.Ino || after.Size != h.Size || after.Uid != before.Uid || after.Nlink != 1 || after.Mode&unix.S_IFMT != unix.S_IFREG {
					err = update.ErrIncompatible
					return
				}
				var b executableInspection
				b, err = inspectPayloadExecutable(ctx, readOnly, req.Identity)
				if err != nil {
					return
				}
				switch role {
				case "app":
					proof.App = b
				case "helper":
					proof.Helper = b
				case "bootstrap":
					proof.Bootstrap = b
				}
			}()
			if err != nil {
				return proof, fmt.Errorf("executable %s: %w", name, err)
			}
		} else {
			if _, err = io.CopyN(io.Discard, tr, h.Size); err != nil {
				return proof, err
			}
		}
	}
	// Do not hide extra nonzero tar records, gzip checksum failures, or limit exhaustion.
	var buf [32768]byte
	for {
		n, err := lr.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return proof, update.ErrIncompatible
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return proof, err
		}
	}
	if lr.N == 0 {
		return proof, update.ErrIncompatible
	}
	for _, entry := range proof.Entries {
		if entry.Metadata != nil && !seen[entry.Metadata.Companion] {
			return proof, update.ErrIncompatible
		}
	}
	proof.DecodedBytes = bounds.Decoded + 1 - lr.N
	sort.Slice(proof.Entries, func(i, j int) bool { return proof.Entries[i].Path < proof.Entries[j].Path })
	return proof, nil
}
func payloadName(raw string) (string, error) {
	if len(raw) > 1024 || strings.ContainsAny(raw, "\x00\r\n\t\\") || strings.HasPrefix(raw, "/") {
		return "", update.ErrIncompatible
	}
	raw = strings.TrimSuffix(raw, "/")
	raw = strings.TrimPrefix(raw, "./")
	if raw == "." || raw == "" {
		return "/", nil
	}
	if path.Clean(raw) != raw || raw == ".." || strings.HasPrefix(raw, "../") {
		return "", update.ErrIncompatible
	}
	return "/" + raw, nil
}
func payloadBinaryRole(name string, kind update.InstallerKind) string {
	if kind == update.InstallerScript {
		switch name {
		case "/stackstead":
			return "app"
		case "/stackstead-updater":
			return "helper"
		case "/stackstead-updater-bootstrap":
			return "bootstrap"
		}
		return ""
	}
	switch name {
	case "/usr/bin/stackstead", "/usr/local/bin/stackstead":
		return "app"
	case "/usr/libexec/stackstead/stackstead-updater", "/Library/PrivilegedHelperTools/stackstead-updater":
		return "helper"
	case "/usr/libexec/stackstead/stackstead-updater-bootstrap", "/Library/PrivilegedHelperTools/stackstead-updater-bootstrap":
		return "bootstrap"
	}
	return ""
}

var stampedIdentity = regexp.MustCompile(`(?:^|[ \t"'])-X(?:=|[ \t]+)github\.com/rcss-labs/stackstead/internal/version\.(Version|Commit)=([^ \t"']+)`)

func slicePayloadIdentity(r io.ReaderAt, osName, arch string) (update.Identity, error) {
	bi, err := buildinfo.Read(r)
	if err != nil {
		return update.Identity{}, err
	}
	id := update.Identity{OS: osName, Arch: arch}
	seen := map[string]bool{}
	for _, s := range bi.Settings {
		if s.Key != "-ldflags" {
			continue
		}
		for _, m := range stampedIdentity.FindAllStringSubmatch(s.Value, -1) {
			if seen[m[1]] {
				return id, update.ErrIncompatible
			}
			seen[m[1]] = true
			switch m[1] {
			case "Version":
				id.Version = strings.TrimPrefix(m[2], "v")
			case "Commit":
				id.Commit = m[2]
			}
		}
	}
	if !nativeStableVersion.MatchString(id.Version) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(id.Commit) {
		return id, update.ErrIncompatible
	}
	return id, nil
}
func inspectPayloadExecutable(ctx context.Context, f *os.File, want update.Identity) (executableInspection, error) {
	proof := executableInspection{}
	var err error
	add := func(r io.ReaderAt, osName, arch string) error {
		id, e := slicePayloadIdentity(r, osName, arch)
		if e != nil {
			return e
		}
		expected := want
		expected.Arch = arch
		if id != expected {
			return update.ErrIncompatible
		}
		proof.Slices = append(proof.Slices, id)
		if arch == want.Arch {
			proof.Identity = id
		}
		return nil
	}
	if want.OS == "darwin" {
		fat, e := macho.NewFatFile(f)
		if e == nil {
			defer fat.Close()
			if len(fat.Arches) != 2 {
				return proof, update.ErrIncompatible
			}
			for _, a := range fat.Arches {
				arch := machoArch(a.Cpu)
				if arch == "" {
					return proof, update.ErrIncompatible
				}
				if err = add(io.NewSectionReader(f, int64(a.Offset), int64(a.Size)), "darwin", arch); err != nil {
					return proof, err
				}
			}
		} else {
			m, e := macho.NewFile(f)
			if e != nil {
				return proof, e
			}
			defer m.Close()
			err = add(f, "darwin", machoArch(m.Cpu))
		}
	} else if want.OS == "linux" {
		m, e := elf.NewFile(f)
		if e != nil {
			return proof, e
		}
		defer m.Close()
		arch := ""
		switch m.Machine {
		case elf.EM_X86_64:
			arch = "amd64"
		case elf.EM_AARCH64:
			arch = "arm64"
		}
		err = add(f, "linux", arch)
	} else {
		return proof, update.ErrIncompatible
	}
	if err != nil || proof.Identity != want {
		return proof, errors.Join(err, update.ErrIncompatible)
	}
	sort.Slice(proof.Slices, func(i, j int) bool { return proof.Slices[i].Arch < proof.Slices[j].Arch })
	if len(proof.Slices) > 1 && proof.Slices[0].Arch == proof.Slices[1].Arch {
		return proof, update.ErrIncompatible
	}
	if want.OS == goruntime.GOOS && want.Arch == goruntime.GOARCH {
		if os.Geteuid() == 0 {
			return proof, update.ErrUnauthorized
		}
		if err = f.Chmod(0700); err != nil {
			return proof, err
		}
		c := exec.CommandContext(ctx, f.Name(), "version", "-o", "json")
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=/", "LANG=C", "LC_ALL=C"}
		c.Dir = "/"
		var b strings.Builder
		c.Stdout = &boundedPayloadWriter{writer: &b, left: childMetadataLimit}
		c.Stderr = io.Discard
		c.WaitDelay = time.Second
		if err = c.Run(); err != nil {
			return proof, err
		}
		var id update.Identity
		dec := json.NewDecoder(strings.NewReader(b.String()))
		if err = dec.Decode(&id); err != nil || dec.Decode(new(any)) != io.EOF {
			return proof, update.ErrIncompatible
		}
		id.Version = strings.TrimPrefix(id.Version, "v")
		if id != want {
			return proof, update.ErrIncompatible
		}
	}
	return proof, nil
}
func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	}
	return ""
}

type boundedPayloadWriter struct {
	writer io.Writer
	left   int64
}

func (w *boundedPayloadWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, update.ErrIncompatible
	}
	n, e := w.writer.Write(b)
	w.left -= int64(n)
	return n, e
}

// Converters never extract paths. Their stdout is consumed with a finite budget;
// every error cancels the process, closes the pipe and waits before returning.
func convertPayloadStream(ctx context.Context, input *os.File, tool string, args []string, limit int64, consume func(io.Reader) error) error {
	if limit <= 0 || limit > 2<<30 {
		return update.ErrIncompatible
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/", "LANG=C", "LC_ALL=C", "COPYFILE_DISABLE=1"}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{input}
	cmd.Stdin = input
	stderr := &childBoundedLog{cancel: cancel}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		stdout.Close()
		return err
	}
	lr := &io.LimitedReader{R: stdout, N: limit + 1}
	err = consume(lr)
	if err == nil {
		_, err = io.Copy(io.Discard, lr)
		if lr.N == 0 {
			err = update.ErrIncompatible
		}
	}
	if err != nil {
		cancel()
		stdout.Close()
	}
	waitErr := cmd.Wait()
	stdout.Close()
	return errors.Join(err, waitErr)
}

func openPayloadArtifact(dir *os.File, name string, owner uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 || platformDescriptorTrustFD(fd) != nil {
		unix.Close(fd)
		return nil, update.ErrSetupRequired
	}
	return os.NewFile(uintptr(fd), name), nil
}
func validatePayloadInventory(p payloadInspection) error {
	if len(p.Entries) == 0 || len(p.Entries) > 4096 || p.DecodedBytes <= 0 || p.DecodedBytes > 2<<30 {
		return update.ErrIncompatible
	}
	seen := map[string]bool{}
	roles := map[string]int{}
	names := 0
	var total int64
	for _, e := range p.Entries {
		names += len(e.Path)
		if len(e.Path) > 1024 || names > 64<<10 || !strings.HasPrefix(e.Path, "/") || path.Clean(e.Path) != e.Path || e.Path == "/" || strings.ContainsAny(e.Path, "\x00\r\n\t\\") || seen[e.Path] || e.Size < 0 || e.Size > childArtifactLimit || (e.Directory && e.Size != 0) {
			return update.ErrIncompatible
		}
		seen[e.Path] = true
		total += e.Size
		if total > 2<<30 {
			return update.ErrIncompatible
		}
		if role := payloadBinaryRole(e.Path, p.Installer); role != "" {
			if e.Directory || e.Metadata != nil {
				return update.ErrIncompatible
			}
			roles[role]++
		}
		if e.Metadata != nil {
			m := e.Metadata
			if p.Installer != update.InstallerPkg || m.Path != e.Path || m.Companion == "/" || m.Path != metadataPath(m.Companion) || m.Attribute != "com.apple.provenance" || m.Size != 163 || e.Size != m.Size || e.Directory {
				return update.ErrIncompatible
			}
		}
	}
	if total > p.DecodedBytes || roles["app"] != 1 || roles["helper"] != 1 || roles["bootstrap"] != 1 {
		return update.ErrIncompatible
	}
	for _, e := range p.Entries {
		if e.Metadata != nil && !seen[e.Metadata.Companion] {
			return update.ErrIncompatible
		}
	}
	return nil
}
