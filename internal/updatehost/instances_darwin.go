package updatehost

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// RegisteredInstances validates every running process using the recorded binary
// against all registered launchd agents. Call again immediately before install;
// process discovery alone cannot prevent a new process starting afterwards.
func RegisteredInstances(ctx context.Context, reg update.Registration) ([]update.Instance, error) {
	return registeredInstances(ctx, reg, NewCommandRunner())
}

type darwinProcess struct {
	pid     int
	uid     uint32
	started string
}

func processRows(out []byte) ([]darwinProcess, error) {
	if len(out) > commandOutputLimit {
		return nil, update.ErrSetupRequired
	}
	var rows []darwinProcess
	seen := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 7 {
			return nil, update.ErrSetupRequired
		}
		pid, e := strconv.Atoi(f[0])
		uid, e2 := strconv.ParseUint(f[1], 10, 32)
		start := strings.Join(f[2:], " ")
		_, e3 := time.Parse("Mon Jan 2 15:04:05 2006", start)
		if e != nil || e2 != nil || e3 != nil || pid <= 0 || seen[pid] {
			return nil, update.ErrSetupRequired
		}
		seen[pid] = true
		rows = append(rows, darwinProcess{pid, uint32(uid), start})
	}
	if len(rows) > 4096 {
		return nil, update.ErrSetupRequired
	}
	return rows, nil
}
func registeredInstances(ctx context.Context, reg update.Registration, runner CommandRunner) ([]update.Instance, error) {
	if runner == nil || reg.Identity.OS != "darwin" || !filepath.IsAbs(reg.Binary) || filepath.Clean(reg.Binary) != reg.Binary || len(reg.Instances) == 0 {
		return nil, update.ErrSetupRequired
	}
	var binary unix.Stat_t
	if unix.Lstat(reg.Binary, &binary) != nil || binary.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, update.ErrSetupRequired
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	supervisor := NewLaunchd(runner)
	known := map[int]update.Instance{}
	ids := map[string]bool{}
	agents := map[string]bool{}
	for _, in := range reg.Instances {
		key := launchdDomain(in) + "/" + in.Supervisor.Name
		if ids[in.ID] || agents[key] {
			return nil, update.ErrSetupRequired
		}
		ids[in.ID] = true
		agents[key] = true
		st, e := supervisor.Probe(ctx, in)
		if e != nil {
			return nil, e
		}
		if st.Running {
			if _, exists := known[st.PID]; exists {
				return nil, update.ErrSetupRequired
			}
			known[st.PID] = in
		}
	}
	out, _, e := runner.Run(ctx, 0, 0, "/bin/ps", []string{"-axo", "pid=,uid=,lstart="}, nil)
	if e != nil {
		return nil, update.ErrSetupRequired
	}
	rows, e := processRows(out)
	if e != nil || len(rows) == 0 {
		return nil, update.ErrSetupRequired
	}
	matched := map[int]bool{}
	for _, process := range rows {
		pid := strconv.Itoa(process.pid)
		text, stderr, probeErr := runner.Run(ctx, 0, 0, "/usr/sbin/lsof", []string{"-n", "-P", "-a", "-p", pid, "-d", "txt", "-F", "pufDin"}, nil)
		uses, proofErr := processUsesBinary(text, process, reg.Binary, &binary)
		// Check liveness and the start timestamp even after successful discovery to
		// reject PID reuse while observing membership.
		check, _, checkErr := runner.Run(ctx, 0, 0, "/bin/ps", []string{"-p", pid, "-o", "pid=,uid=,lstart="}, nil)
		live, parseErr := processRows(check)
		var exit *exec.ExitError
		if len(live) == 0 && parseErr == nil && errors.As(checkErr, &exit) && exit.ExitCode() == 1 {
			continue
		}
		if checkErr != nil || parseErr != nil || len(live) != 1 || live[0] != process || probeErr != nil || len(stderr) != 0 || proofErr != nil {
			return nil, update.ErrSetupRequired
		}
		if uses {
			in, ok := known[process.pid]
			if !ok || in.UID != process.uid {
				return nil, update.ErrSetupRequired
			}
			matched[process.pid] = true
		}
	}
	for pid := range known {
		if !matched[pid] {
			return nil, update.ErrSetupRequired
		}
	}
	return append([]update.Instance(nil), reg.Instances...), nil
}
func processUsesBinary(out []byte, process darwinProcess, path string, binary *unix.Stat_t) (bool, error) {
	if len(out) == 0 || len(out) > commandOutputLimit {
		return false, update.ErrSetupRequired
	}
	fields := map[byte]string{}
	files, uses := 0, false
	header := true
	finish := func() error {
		if header {
			return nil
		}
		if len(fields) != 4 || fields['f'] != "txt" || fields['n'] == "" || !strings.HasPrefix(fields['D'], "0x") {
			return update.ErrSetupRequired
		}
		dev, e := strconv.ParseUint(fields['D'][2:], 16, 64)
		inode, e2 := strconv.ParseUint(fields['i'], 10, 64)
		if e != nil || e2 != nil || inode == 0 {
			return update.ErrSetupRequired
		}
		if fields['n'] == path || dev == uint64(uint32(binary.Dev)) && inode == binary.Ino {
			uses = true
		}
		files++
		return nil
	}
	seenPID, seenUID := false, false
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if len(line) < 2 {
			return false, update.ErrSetupRequired
		}
		key, value := line[0], line[1:]
		if key == 'p' {
			if seenPID || !header || value != strconv.Itoa(process.pid) {
				return false, update.ErrSetupRequired
			}
			seenPID = true
			continue
		}
		if key == 'u' {
			if seenUID || !header || value != strconv.FormatUint(uint64(process.uid), 10) {
				return false, update.ErrSetupRequired
			}
			seenUID = true
			continue
		}
		if key == 'f' {
			if e := finish(); e != nil {
				return false, e
			}
			fields = map[byte]string{}
			header = false
		}
		if header || key != 'f' && key != 'D' && key != 'i' && key != 'n' {
			return false, update.ErrSetupRequired
		}
		if _, exists := fields[key]; exists {
			return false, update.ErrSetupRequired
		}
		fields[key] = value
	}
	if e := finish(); e != nil || !seenPID || !seenUID || files == 0 {
		return false, update.ErrSetupRequired
	}
	return uses, nil
}
