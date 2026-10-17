package updatehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

const commandOutputLimit = 64 << 10

var instanceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,180}\.service$`)

type commandRunner struct{ custom map[string]bool }

// NewCommandRunner accepts custom paths only from root-validated registrations.
// Every execution revalidates ownership and permits only the adapter protocol.
func NewCommandRunner(customAdapters ...string) CommandRunner {
	r := commandRunner{custom: map[string]bool{}}
	for _, path := range customAdapters {
		r.custom[path] = true
	}
	return r
}
func (r commandRunner) Run(ctx context.Context, uid, gid uint32, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(stdin) > commandOutputLimit {
		return nil, nil, update.ErrUnauthorized
	}
	if uid == 0 {
		if gid != 0 || !r.allowedRoot(path, args) {
			return nil, nil, update.ErrUnauthorized
		}
		if err := trustedExecutable(path); err != nil {
			return nil, nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "HOME=/", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0", "DEBIAN_FRONTEND=noninteractive"}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}}
	cmd.Stdin = bytes.NewReader(stdin)
	out, errout := &boundedOutput{limit: commandOutputLimit}, &boundedOutput{limit: commandOutputLimit}
	cmd.Stdout = out
	cmd.Stderr = errout
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if out.exceeded || errout.exceeded {
		err = errors.Join(err, update.ErrIncompatible)
	}
	return sanitizeOutput(out.data), sanitizeOutput(errout.data), err
}
func (r commandRunner) allowedRoot(path string, args []string) bool {
	if path == "/usr/bin/systemctl" {
		if len(args) == 5 && args[0] == "--system" && args[1] == "--no-ask-password" && (args[2] == "stop" || args[2] == "start") && args[3] == "--" && serviceName.MatchString(args[4]) {
			return true
		}
		return len(args) == 7 && args[0] == "--system" && args[1] == "--no-ask-password" && args[2] == "show" && args[3] == "--property=LoadState,ActiveState,UnitFileState,MainPID" && args[4] == "--no-pager" && args[5] == "--" && serviceName.MatchString(args[6])
	}
	if path == "/usr/bin/dpkg-query" {
		return len(args) == 3 && args[0] == "-S" && args[1] == "--" && filepath.IsAbs(args[2]) || len(args) == 4 && args[0] == "--show" && args[1] == "--showformat="+debQueryFormat && args[2] == "--" && args[3] == "stackstead"
	}
	if path == "/usr/bin/dpkg-deb" {
		return len(args) == 3 && args[0] == "--show" && args[1] == "--showformat="+debArtifactFormat && debArtifactPath(args[2])
	}
	if path == "/usr/bin/dpkg" {
		if len(args) == 5 && args[0] == "--dry-run" {
			args = args[1:]
		}
		return len(args) == 4 && args[0] == "--force-confdef" && args[1] == "--force-confold" && args[2] == "--install" && debArtifactPath(args[3])
	}

	if path == "/usr/bin/rpm" {
		return len(args) == 3 && args[0] == "-qf" && args[1] == "--" && filepath.IsAbs(args[2])
	}
	if path == "/sbin/apk" {
		return len(args) == 4 && args[0] == "info" && args[1] == "--who-owns" && args[2] == "--" && filepath.IsAbs(args[3])
	}
	return r.custom[path] && len(args) == 2 && (args[0] == "probe" || args[0] == "stop" || args[0] == "start") && instanceID.MatchString(args[1])
}

type boundedOutput struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := b.limit - len(b.data)
	if len(p) > left {
		b.exceeded = true
		p = p[:left]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func sanitizeOutput(p []byte) []byte {
	return []byte(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) && r != '\uFFFD' {
			return r
		}
		return -1
	}, string(p)))
}

// Walk descriptors from / so neither a symlink nor a writable parent can redirect
// a privileged command or installation. Tests can anchor at their own directory.
func trustedDirectory(path string, owner uint32, anchor string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, update.ErrSetupRequired
	}
	start, relative := "/", strings.TrimPrefix(path, "/")
	if anchor != "" {
		var err error
		relative, err = filepath.Rel(anchor, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return nil, update.ErrSetupRequired
		}
		start = anchor
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, update.ErrSetupRequired
	}
	current := os.NewFile(uintptr(fd), start)
	check := func(f *os.File) error {
		var st unix.Stat_t
		if unix.Fstat(int(f.Fd()), &st) != nil || st.Uid != owner || st.Mode&0022 != 0 {
			return update.ErrSetupRequired
		}
		return nil
	}
	if err = check(current); err != nil {
		current.Close()
		return nil, err
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." {
			continue
		}
		next, e := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		current.Close()
		if e != nil {
			return nil, update.ErrSetupRequired
		}
		current = os.NewFile(uintptr(next), part)
		if e = check(current); e != nil {
			current.Close()
			return nil, e
		}
	}
	return current, nil
}
func trustedExecutable(path string) error {
	d, err := trustedDirectory(filepath.Dir(path), 0, "")
	if err != nil {
		return err
	}
	defer d.Close()
	fd, err := unix.Openat(int(d.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return update.ErrSetupRequired
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 {
		return update.ErrSetupRequired
	}
	return nil
}
