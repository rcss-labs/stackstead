package updatehost

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

const childMetadataLimit = 64 << 10
const childArtifactLimit int64 = 512 << 20
const childConfigLimit int64 = 1 << 20

type childRole string

const (
	childInspectDB      childRole = "inspect-db"
	childSnapshotDB     childRole = "snapshot-db"
	childRestoreDB      childRole = "restore-db"
	childWritePrefix    childRole = "write-prefix"
	childInspectPayload childRole = "inspect-payload"
)

// No executable, argv, backup, archive or output path is accepted by this protocol.
// Instance paths originate only in the root-held registration.
type childRequest struct {
	Protocol int             `json:"protocol"`
	Role     childRole       `json:"role"`
	JobID    string          `json:"jobID"`
	Instance update.Instance `json:"instance"`
	Limit    int64           `json:"limit,omitempty"`
	Expected snapshotProof   `json:"expected,omitempty"`
	Payload  payloadRequest  `json:"payload,omitempty"`
}
type childResponse struct {
	Protocol   int                `json:"protocol"`
	Error      string             `json:"error,omitempty"`
	Inspection *dbInspection      `json:"inspection,omitempty"`
	Snapshot   *snapshotProof     `json:"snapshot,omitempty"`
	Payload    *payloadInspection `json:"payload,omitempty"`
}

var childDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func writeChildRequest(w io.Writer, request childRequest) error {
	b, e := json.Marshal(request)
	if e != nil {
		return e
	}
	return writeChildFrame(w, b)
}
func writeChildResponse(w io.Writer, response childResponse) error {
	b, e := json.Marshal(response)
	if e != nil {
		return e
	}
	return writeChildFrame(w, b)
}
func writeChildFrame(w io.Writer, b []byte) error {
	if len(b) > childMetadataLimit {
		return update.ErrIncompatible
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	if _, e := w.Write(header[:]); e != nil {
		return e
	}
	_, e := w.Write(b)
	return e
}
func readChildFrame[T childRequest | childResponse](r io.Reader, v *T) error {
	var header [4]byte
	if _, e := io.ReadFull(r, header[:]); e != nil {
		return e
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > childMetadataLimit {
		return update.ErrIncompatible
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(r, b); e != nil {
		return e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return update.ErrIncompatible
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return update.ErrIncompatible
	}
	return nil
}
func validChildRequest(r childRequest) error {
	if r.Protocol != 1 || !instanceID.MatchString(r.JobID) || !instanceID.MatchString(r.Instance.ID) || r.Instance.UID == 0 {
		return update.ErrUnauthorized
	}
	if r.Role != childInspectPayload && r.Payload != (payloadRequest{}) {
		return update.ErrUnauthorized
	}
	switch r.Role {
	case childInspectDB:
		if r.Limit != 0 || r.Expected != (snapshotProof{}) {
			return update.ErrUnauthorized
		}
	case childSnapshotDB:
		if r.Limit <= 0 || r.Limit >= int64(^uint64(0)>>1) || r.Expected != (snapshotProof{}) {
			return update.ErrUnauthorized
		}
	case childRestoreDB:
		p := r.Expected
		if r.Limit != p.Size || p.Size <= 0 || p.Budget < p.Size || p.Budget >= int64(^uint64(0)>>1) || p.Schema < 0 || !childDigest.MatchString(p.SHA256) || !childDigest.MatchString(p.ConfigSHA256) {
			return update.ErrUnauthorized
		}
	case childInspectPayload:
		p := r.Payload
		if r.Limit != p.Artifact.Size || r.Limit <= 0 || r.Limit > childArtifactLimit || !childDigest.MatchString(p.Artifact.SHA256) || r.Expected != (snapshotProof{}) || r.Instance.DataDir != "" || r.Instance.ConfigPath != "" {
			return update.ErrUnauthorized
		}
		switch p.Installer {
		case update.InstallerScript, update.InstallerDeb, update.InstallerRPM, update.InstallerPkg:
		default:
			return update.ErrUnauthorized
		}
		if p.Identity.OS != "linux" && p.Identity.OS != "darwin" {
			return update.ErrUnauthorized
		}
		if p.Identity.Arch != "amd64" && p.Identity.Arch != "arm64" {
			return update.ErrUnauthorized
		}
		return nil
	case childWritePrefix:
		if r.Limit <= 0 || r.Limit > childArtifactLimit || r.Expected != (snapshotProof{}) || r.Instance.DataDir != "" || r.Instance.ConfigPath != "" {
			return update.ErrUnauthorized
		}
		return nil
	default:
		return update.ErrUnauthorized
	}
	for _, p := range []string{r.Instance.DataDir, r.Instance.ConfigPath} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return update.ErrUnauthorized
		}
	}
	return nil
}

// Inherited role handles lack CLOEXEC; runtime-created epoll/kqueue handles have
// CLOEXEC and must never be closed by sweeping an arbitrary numeric FD range.
func childRoleFiles(role childRole) (input, output, dir *os.File, err error) {
	allowed := map[int]bool{3: true}
	switch role {
	case childSnapshotDB:
		allowed[5] = true
	case childRestoreDB, childInspectPayload:
		allowed[4] = true
	case childWritePrefix:
		allowed[4] = true
		allowed[6] = true
	}
	inherited, e := inheritedChildFDs()
	if e != nil {
		return nil, nil, nil, e
	}
	for _, fd := range inherited {
		if fd <= 2 {
			continue
		}
		if !allowed[fd] {
			unix.Close(fd)
			return nil, nil, nil, update.ErrUnauthorized
		}
	}
	for fd := range allowed {
		flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC != 0 {
			return nil, nil, nil, update.ErrUnauthorized
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil {
			return nil, nil, nil, update.ErrUnauthorized
		}
		if fd == 6 {
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				return nil, nil, nil, update.ErrUnauthorized
			}
		} else if fd != 3 {
			if st.Mode&unix.S_IFMT != unix.S_IFIFO && !((role == childWritePrefix || role == childInspectPayload) && fd == 4 && st.Mode&unix.S_IFMT == unix.S_IFREG) {
				return nil, nil, nil, update.ErrUnauthorized
			}
		}
	}
	if allowed[4] {
		input = os.NewFile(4, "child-input")
	}
	if allowed[5] {
		output = os.NewFile(5, "child-output")
	}
	if allowed[6] {
		dir = os.NewFile(6, "child-directory")
	}
	return
}
