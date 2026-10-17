package updatehost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
	"golang.org/x/sys/unix"
)

type childExecutor struct {
	workerPath, workerDigest, socketRoot, anchor string
	owner                                        uint32
	// Set only by package tests; production always uses the strict credential drop.
	testCredentialHook func(*exec.Cmd) error
	testStderr         func([]byte)
}
type dbInspection struct {
	Schema       int    `json:"schema"`
	ConfigSHA256 string `json:"configSHA256"`
	DBBytes      int64  `json:"dbBytes"`
	WALBytes     int64  `json:"walBytes"`
	SHMBytes     int64  `json:"shmBytes"`
	FreeBytes    uint64 `json:"freeBytes"`
	UID          uint32 `json:"uid"`
	GID          uint32 `json:"gid"`
}
type snapshotProof struct {
	Budget       int64  `json:"budget"`
	Size         int64  `json:"size"`
	Schema       int    `json:"schema"`
	SHA256       string `json:"sha256"`
	ConfigSHA256 string `json:"configSHA256"`
}

// RunInternalChild is solely the fixed internal-child entrypoint. No operation,
// path or credential can be supplied through its argv or environment.
func RunInternalChild(ctx context.Context) error { return runInternalChild(ctx, 0) }
func runInternalChild(ctx context.Context, expectedPeer uint32) error {
	// Authentication precedes request parsing, handle inspection or all app I/O.
	if os.Geteuid() == 0 {
		return update.ErrUnauthorized
	}
	uid, err := childPeerUID(3)
	if err != nil || uid != expectedPeer {
		return update.ErrUnauthorized
	}
	control := os.NewFile(3, "child-control")
	defer control.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { control.Close() })
	defer stop()
	var request childRequest
	if err = readChildFrame(control, &request); err != nil {
		return err
	}
	if err = validChildRequest(request); err != nil {
		return err
	}
	if request.Instance.UID != uint32(os.Geteuid()) || request.Instance.GID != uint32(os.Getegid()) {
		return update.ErrUnauthorized
	}
	input, output, dir, err := childRoleFiles(request.Role)
	if err != nil {
		return err
	}
	for _, f := range []*os.File{input, output, dir} {
		if f != nil {
			defer f.Close()
		}
	}
	if err = childResourceLimits(request); err != nil {
		return err
	}
	response := childResponse{Protocol: 1}
	switch request.Role {
	case childInspectDB:
		var proof dbInspection
		proof, err = inspectChildDB(ctx, request.Instance)
		if err == nil {
			response.Inspection = &proof
		}
	case childSnapshotDB:
		var proof snapshotProof
		proof, err = snapshotChildDB(ctx, request.Instance, request.Limit, output)
		if err == nil {
			response.Snapshot = &proof
		}
	case childRestoreDB:
		err = restoreChildDB(ctx, request.JobID, request.Instance, request.Expected, input)
	case childInspectPayload:
		var proof payloadInspection
		proof, err = inspectChildPayload(ctx, request.Payload, input)
		if err == nil {
			response.Payload = &proof
		}
	case childWritePrefix:
		err = writeChildPrefix(ctx, request.Instance, request.Limit, input, dir)
	default:
		err = update.ErrUnauthorized
	}
	// Paths, SQLite diagnostics and config bytes never cross the control channel.
	if err != nil {
		response.Error = "child-operation-failed"
	}
	if e := writeChildResponse(control, response); e != nil {
		return e
	}
	return err
}
func (e *childExecutor) InspectDB(ctx context.Context, jobID string, in update.Instance) (dbInspection, error) {
	response, err := e.execute(ctx, childRequest{Protocol: 1, Role: childInspectDB, JobID: jobID, Instance: in}, nil, nil, nil)
	if err != nil {
		return dbInspection{}, err
	}
	if response.Inspection == nil || response.Snapshot != nil {
		return dbInspection{}, update.ErrIncompatible
	}
	return *response.Inspection, nil
}
func (e *childExecutor) Snapshot(ctx context.Context, jobID string, in update.Instance, limit int64, output *os.File) (snapshotProof, error) {
	if output == nil || limit <= 0 || limit >= int64(^uint64(0)>>1) {
		return snapshotProof{}, update.ErrUnauthorized
	}
	var destination unix.Stat_t
	if unix.Fstat(int(output.Fd()), &destination) != nil || destination.Mode&unix.S_IFMT != unix.S_IFREG || destination.Uid != e.owner || destination.Mode&0777 != 0600 || destination.Nlink != 1 || destination.Size != 0 {
		return snapshotProof{}, update.ErrUnauthorized
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return snapshotProof{}, err
	}
	response, err := e.execute(ctx, childRequest{Protocol: 1, Role: childSnapshotDB, JobID: jobID, Instance: in, Limit: limit}, nil, output, nil)
	if err != nil {
		return snapshotProof{}, err
	}
	if response.Snapshot == nil || response.Inspection != nil {
		return snapshotProof{}, update.ErrIncompatible
	}
	proof := *response.Snapshot
	st, err := output.Stat()
	if err != nil || st.Size() != proof.Size || proof.Size <= 0 || proof.Size > limit || proof.Budget != limit || !childDigest.MatchString(proof.SHA256) || !childDigest.MatchString(proof.ConfigSHA256) {
		return snapshotProof{}, update.ErrIncompatible
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		return snapshotProof{}, err
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(output, limit+1))
	if err != nil || n != proof.Size || hex.EncodeToString(digest.Sum(nil)) != proof.SHA256 {
		return snapshotProof{}, update.ErrIncompatible
	}
	if err = output.Sync(); err != nil {
		return snapshotProof{}, err
	}
	return proof, nil
}
func (e *childExecutor) Restore(ctx context.Context, jobID string, in update.Instance, expected snapshotProof, input *os.File) error {
	if input == nil {
		return update.ErrUnauthorized
	}
	_, err := e.execute(ctx, childRequest{Protocol: 1, Role: childRestoreDB, JobID: jobID, Instance: in, Limit: expected.Size, Expected: expected}, input, nil, nil)
	return err
}
func (e *childExecutor) execute(ctx context.Context, request childRequest, input, output, dir *os.File) (childResponse, error) {
	var response childResponse
	if err := validChildRequest(request); err != nil {
		return response, err
	}
	if e.testCredentialHook == nil && (os.Geteuid() != 0 || e.owner != 0) {
		return response, update.ErrUnauthorized
	}
	path, err := e.publishChild()
	if err != nil {
		return response, err
	}
	timeout := 10 * time.Minute
	if request.Role == childInspectPayload {
		timeout = payloadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return response, err
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	parent := os.NewFile(uintptr(pair[0]), "parent-control")
	defer parent.Close()
	child := os.NewFile(uintptr(pair[1]), "child-control")
	defer child.Close()
	var inputRead, inputWrite, outputRead, outputWrite *os.File
	defer func() {
		for _, f := range []*os.File{inputRead, inputWrite, outputRead, outputWrite} {
			if f != nil {
				f.Close()
			}
		}
	}()
	files := []*os.File{child}
	switch request.Role {
	case childSnapshotDB:
		outputRead, outputWrite, err = os.Pipe()
		files = append(files, nil, outputWrite)
	case childRestoreDB:
		inputRead, inputWrite, err = os.Pipe()
		files = append(files, inputRead)
	case childInspectPayload:
		if input == nil {
			return response, update.ErrUnauthorized
		}
		files = append(files, input)
	case childWritePrefix:
		files = append(files, input, nil, dir)
	}
	if err != nil {
		return response, err
	}
	if request.Role == childWritePrefix && (input == nil || dir == nil) {
		return response, update.ErrUnauthorized
	}
	cmd := exec.CommandContext(ctx, path, "internal-child")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = files
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: request.Instance.UID, Gid: request.Instance.GID, Groups: []uint32{}}}
	if e.testCredentialHook != nil {
		if err = e.testCredentialHook(cmd); err != nil {
			return response, err
		}
	}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		}
		return os.ErrProcessDone
	}
	stderr := &childBoundedLog{cancel: cancel}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		return response, err
	}
	// Native converters stay inside this owned group. Clean surviving descendants
	// even when the fixed child reports an ordinary operation failure and exits.
	cleanupPending := true
	cleanupGroup := func() {
		if cleanupPending {
			cleanupPending = false
			if cmd.Process.Pid > 1 {
				_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
			}
		}
	}
	defer cleanupGroup()
	child.Close()
	if inputRead != nil {
		inputRead.Close()
	}
	if outputWrite != nil {
		outputWrite.Close()
	}
	stop := context.AfterFunc(ctx, func() {
		parent.Close()
		if inputWrite != nil {
			inputWrite.Close()
		}
		if outputRead != nil {
			outputRead.Close()
		}
	})
	defer stop()
	controlDone := make(chan error, 1)
	go func() {
		err := writeChildRequest(parent, request)
		if err == nil {
			err = readChildFrame(parent, &response)
		}
		if err != nil {
			cancel()
		}
		controlDone <- err
	}()
	streamDone := make(chan error, 1)
	switch request.Role {
	case childSnapshotDB:
		go func() {
			err := copyChildBounded(output, outputRead, request.Limit)
			if err != nil {
				cancel()
			}
			streamDone <- err
		}()
	case childRestoreDB:
		go func() {
			err := copyChildBounded(inputWrite, input, request.Limit)
			inputWrite.Close()
			if err != nil {
				cancel()
			}
			streamDone <- err
		}()
	default:
		streamDone <- nil
	}
	waitErr := cmd.Wait() // Always reap, including deadline, metadata or stream overflow.
	cleanupGroup()        // Do not delay cleanup behind control or stream result handling.
	if e.testStderr != nil {
		e.testStderr(stderr.data)
	}
	controlErr := <-controlDone
	streamErr := <-streamDone
	if err = errors.Join(waitErr, controlErr, streamErr); err != nil {
		return childResponse{}, fmt.Errorf("fixed child failed: %w", err)
	}
	if response.Protocol != 1 || response.Error != "" || stderr.exceeded {
		return childResponse{}, update.ErrRecoveryRequired
	}
	return response, nil
}

type childBoundedLog struct {
	data     []byte
	size     int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *childBoundedLog) Write(p []byte) (int, error) {
	b.size += len(p)
	if b.size > childMetadataLimit {
		b.exceeded = true
		b.cancel()
		return 0, update.ErrIncompatible
	}
	b.data = append(b.data, sanitizeOutput(p)...)
	return len(p), nil
}
func copyChildBounded(dst io.Writer, src io.Reader, limit int64) error {
	if limit <= 0 || limit == int64(^uint64(0)>>1) {
		return update.ErrIncompatible
	}
	if _, err := io.Copy(dst, io.LimitReader(src, limit)); err != nil {
		return err
	}
	var extra [1]byte
	n, err := src.Read(extra[:])
	if n != 0 {
		return update.ErrIncompatible
	}
	if err != io.EOF {
		return errors.Join(err, update.ErrIncompatible)
	}
	return nil
}

// publishChild never executes an app-prefix or root0700 path after dropping UID.
// The sole source is the trusted pinned worker selected by root-held journal state.
func (e *childExecutor) publishChild() (string, error) {
	if !childDigest.MatchString(e.workerDigest) {
		return "", update.ErrUnauthorized
	}
	srcDir, err := trustedDirectory(filepath.Dir(e.workerPath), e.owner, e.anchor)
	if err != nil {
		return "", err
	}
	defer srcDir.Close()
	src, err := openTrustedChildFile(srcDir, filepath.Base(e.workerPath), e.owner, 0)
	if err != nil {
		return "", err
	}
	defer src.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(src, childArtifactLimit+1))
	if err != nil || n <= 0 || n > childArtifactLimit || hex.EncodeToString(digest.Sum(nil)) != e.workerDigest {
		return "", update.ErrIncompatible
	}
	root := e.socketRoot
	if root == "" {
		root = updateipc.SocketRoot
	}
	if strings.HasPrefix(root, "/var/run/") {
		root = "/private" + root
	}
	parent, err := trustedDirectory(filepath.Dir(root), e.owner, e.anchor)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	runtime, err := childDirectory(parent, filepath.Base(root), e.owner)
	if err != nil {
		return "", err
	}
	defer runtime.Close()
	children, err := childDirectory(runtime, "children", e.owner)
	if err != nil {
		return "", err
	}
	defer children.Close()
	dest, err := childDirectory(children, e.workerDigest, e.owner)
	if err != nil {
		return "", err
	}
	defer dest.Close()
	final := filepath.Join(root, "children", e.workerDigest, "child")
	if existing, err := openTrustedChildFile(dest, "child", e.owner, 0755); err == nil {
		defer existing.Close()
		h := sha256.New()
		size, err := io.Copy(h, io.LimitReader(existing, childArtifactLimit+1))
		if err != nil || size != n || hex.EncodeToString(h.Sum(nil)) != e.workerDigest {
			return "", update.ErrIncompatible
		}
		return final, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Link publication is no-replace and permits concurrent identical publishers.
	var nonce [16]byte
	if _, err = io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", err
	}
	name := ".child-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(dest.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0700)
	if err != nil {
		return "", err
	}
	temp := os.NewFile(uintptr(fd), name)
	defer unix.Unlinkat(int(dest.Fd()), name, 0)
	if _, err = src.Seek(0, io.SeekStart); err == nil {
		_, err = io.CopyN(temp, src, n)
	}
	if err == nil {
		err = temp.Chmod(0755)
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return "", err
	}
	if err = unix.Linkat(int(dest.Fd()), name, int(dest.Fd()), "child", 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return e.publishChild()
		}
		return "", err
	}
	if err = dest.Sync(); err != nil {
		return "", err
	}
	published, err := openTrustedChildFile(dest, "child", e.owner, 0755)
	if err != nil {
		return "", err
	}
	defer published.Close()
	copied := sha256.New()
	size, err := io.Copy(copied, io.LimitReader(published, childArtifactLimit+1))
	if err != nil || size != n || hex.EncodeToString(copied.Sum(nil)) != e.workerDigest {
		return "", update.ErrIncompatible
	}
	return final, nil
}
func childDirectory(parent *os.File, name string, owner uint32) (*os.File, error) {
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0755); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if created {
		if err = unix.Fchmod(fd, 0755); err != nil {
			unix.Close(fd)
			return nil, err
		}
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&07777 != 0755 || platformDescriptorTrustFD(fd) != nil {
		unix.Close(fd)
		return nil, update.ErrSetupRequired
	}
	if err = parent.Sync(); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func openTrustedChildFile(dir *os.File, name string, owner uint32, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0100 == 0 || (mode != 0 && uint32(st.Mode)&07777 != mode) || platformDescriptorTrustFD(fd) != nil {
		unix.Close(fd)
		return nil, update.ErrSetupRequired
	}
	return os.NewFile(uintptr(fd), name), nil
}
