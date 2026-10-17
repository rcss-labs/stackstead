package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
	"strconv"
	"strings"
)

type systemdSupervisor struct{ runner CommandRunner }

func NewSystemd(runner CommandRunner) Supervisor { return &systemdSupervisor{runner} }
func (s *systemdSupervisor) valid(in update.Instance) error {
	if s.runner == nil || in.Supervisor.Kind != "systemd" || !instanceID.MatchString(in.ID) || !serviceName.MatchString(in.Supervisor.Name) {
		return update.ErrSetupRequired
	}
	return nil
}
func (s *systemdSupervisor) Probe(ctx context.Context, in update.Instance) (update.ServiceState, error) {
	state := update.ServiceState{InstanceID: in.ID}
	if err := s.valid(in); err != nil {
		return state, err
	}
	out, _, err := s.runner.Run(ctx, 0, 0, "/usr/bin/systemctl", []string{"--system", "--no-ask-password", "show", "--property=LoadState,ActiveState,UnitFileState,MainPID", "--no-pager", "--", in.Supervisor.Name}, nil)
	if err != nil {
		return state, err
	}
	if len(out) > commandOutputLimit {
		return state, update.ErrIncompatible
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || fields[k] != "" {
			return state, update.ErrUnavailable
		}
		fields[k] = v
	}
	if len(fields) != 4 {
		return state, update.ErrUnavailable
	}
	switch fields["LoadState"] {
	case "loaded":
		state.Loaded = true
	case "masked":
		state.Masked = true
	case "not-found":
	default:
		return state, update.ErrUnavailable
	}
	switch fields["UnitFileState"] {
	case "enabled", "enabled-runtime", "linked", "linked-runtime":
		state.Enabled = true
	case "masked", "masked-runtime":
		state.Masked = true
	case "disabled", "static", "indirect", "", "generated", "transient":
	default:
		return state, update.ErrUnavailable
	}
	switch fields["ActiveState"] {
	case "active":
		state.Running = true
	case "inactive", "failed":
	default:
		return state, update.ErrUnavailable
	}
	state.PID, err = strconv.Atoi(fields["MainPID"])
	if err != nil || state.PID < 0 || state.Running && state.PID == 0 || !state.Running && state.PID != 0 {
		return state, update.ErrUnavailable
	}
	return state, nil
}
func (s *systemdSupervisor) Stop(ctx context.Context, in update.Instance) error {
	if err := s.valid(in); err != nil {
		return err
	}
	_, _, err := s.runner.Run(ctx, 0, 0, "/usr/bin/systemctl", []string{"--system", "--no-ask-password", "stop", "--", in.Supervisor.Name}, nil)
	return err
}
func (s *systemdSupervisor) Start(ctx context.Context, in update.Instance, state update.ServiceState) error {
	if err := s.valid(in); err != nil {
		return err
	}
	if state.InstanceID != in.ID {
		return update.ErrUnauthorized
	}
	if !state.Running || state.Masked || !state.Loaded {
		return nil
	}
	_, _, err := s.runner.Run(ctx, 0, 0, "/usr/bin/systemctl", []string{"--system", "--no-ask-password", "start", "--", in.Supervisor.Name}, nil)
	return err
}
