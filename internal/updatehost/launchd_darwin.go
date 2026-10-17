package updatehost

import (
	"context"
	"errors"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

var launchdLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,180}$`)

type launchdSupervisor struct {
	runner CommandRunner
	home   func(uint32) (string, error)
}

// NewLaunchd controls only the GUI domain of each root-registered user. It never
// selects the console user or loads an agent that was originally stopped.
func NewLaunchd(runner CommandRunner) Supervisor {
	return &launchdSupervisor{runner: runner, home: func(uid uint32) (string, error) {
		u, e := user.LookupId(strconv.FormatUint(uint64(uid), 10))
		if e != nil {
			return "", e
		}
		return u.HomeDir, nil
	}}
}
func (s *launchdSupervisor) valid(in update.Instance) error {
	if s.runner == nil || in.UID == 0 || !instanceID.MatchString(in.ID) || in.Supervisor.Kind != "launchd" || !launchdLabel.MatchString(in.Supervisor.Name) || in.Supervisor.AdapterPath != "" {
		return update.ErrSetupRequired
	}
	return nil
}
func launchdDomain(in update.Instance) string { return "gui/" + strconv.FormatUint(uint64(in.UID), 10) }
func (s *launchdSupervisor) run(ctx context.Context, in update.Instance, args ...string) ([]byte, []byte, error) {
	return s.runner.Run(ctx, in.UID, in.GID, "/bin/launchctl", args, nil)
}
func launchdMissing(out, stderr []byte, err error) bool {
	var exit *exec.ExitError
	if len(out) != 0 || !errors.As(err, &exit) {
		return false
	}
	message := strings.TrimSpace(string(stderr))
	return strings.HasPrefix(message, "Could not find domain for") || strings.HasPrefix(message, "Could not find service ")
}
func (s *launchdSupervisor) Probe(ctx context.Context, in update.Instance) (update.ServiceState, error) {
	st := update.ServiceState{InstanceID: in.ID}
	if e := s.valid(in); e != nil {
		return st, e
	}
	out, stderr, e := s.run(ctx, in, "print", launchdDomain(in)+"/"+in.Supervisor.Name)
	if e != nil {
		if launchdMissing(out, stderr, e) {
			return st, nil
		}
		return st, update.ErrSetupRequired
	}
	if len(out) > commandOutputLimit {
		return st, update.ErrSetupRequired
	}
	// launchctl print is explicitly not an API. Parse only the bounded properties
	// needed here and refuse unknown/contradictory states rather than guessing.
	depth, seenState, seenPID := 0, false, false
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if depth == 1 {
			if value, ok := strings.CutPrefix(line, "state = "); ok {
				if seenState {
					return st, update.ErrSetupRequired
				}
				seenState = true
				switch value {
				case "running":
					st.Running = true
				case "not running", "waiting", "exited":
				default:
					return st, update.ErrSetupRequired
				}
			}
			if value, ok := strings.CutPrefix(line, "pid = "); ok {
				if seenPID {
					return st, update.ErrSetupRequired
				}
				seenPID = true
				st.PID, e = strconv.Atoi(value)
				if e != nil || st.PID <= 0 {
					return st, update.ErrSetupRequired
				}
			}
		}
		if strings.HasSuffix(line, "{") {
			depth++
		}
		if line == "}" {
			depth--
		}
		if depth < 0 {
			return st, update.ErrSetupRequired
		}
	}
	if depth != 0 || !seenState || st.Running != seenPID {
		return st, update.ErrSetupRequired
	}
	st.Loaded = true
	out, _, e = s.run(ctx, in, "print-disabled", launchdDomain(in))
	if e != nil || len(out) > commandOutputLimit {
		return st, update.ErrSetupRequired
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "disabled services = {" || strings.TrimSpace(lines[len(lines)-1]) != "}" {
		return st, update.ErrSetupRequired
	}
	st.Enabled = true
	seen := false
	for _, line := range lines[1 : len(lines)-1] {
		label, value, ok := strings.Cut(strings.TrimSpace(line), " => ")
		if !ok {
			return st, update.ErrSetupRequired
		}
		if value != "true" && value != "false" {
			return st, update.ErrSetupRequired
		}
		if label == strconv.Quote(in.Supervisor.Name) {
			if seen {
				return st, update.ErrSetupRequired
			}
			seen = true
			st.Enabled = value == "false"
		}
	}
	if st.Running && !st.Enabled {
		return st, update.ErrSetupRequired
	}
	return st, nil
}
func (s *launchdSupervisor) Stop(ctx context.Context, in update.Instance) error {
	if e := s.valid(in); e != nil {
		return e
	}
	out, stderr, e := s.run(ctx, in, "bootout", launchdDomain(in)+"/"+in.Supervisor.Name)
	if launchdMissing(out, stderr, e) {
		return nil
	}
	return e
}
func (s *launchdSupervisor) Start(ctx context.Context, in update.Instance, st update.ServiceState) error {
	if e := s.valid(in); e != nil {
		return e
	}
	if st.InstanceID != in.ID {
		return update.ErrUnauthorized
	}
	if !st.Running || !st.Loaded || st.Masked {
		return nil
	}
	if !st.Enabled {
		return update.ErrSetupRequired
	}
	current, e := s.Probe(ctx, in)
	if e != nil {
		return e
	}
	if !current.Loaded {
		if s.home == nil {
			return update.ErrSetupRequired
		}
		home, e := s.home(in.UID)
		if e != nil || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
			return update.ErrSetupRequired
		}
		// launchctl itself reads this home path after credentials are dropped.
		if _, _, e = s.run(ctx, in, "bootstrap", launchdDomain(in), filepath.Join(home, "Library/LaunchAgents", in.Supervisor.Name+".plist")); e != nil {
			return e
		}
	}
	_, _, e = s.run(ctx, in, "kickstart", launchdDomain(in)+"/"+in.Supervisor.Name)
	return e
}
