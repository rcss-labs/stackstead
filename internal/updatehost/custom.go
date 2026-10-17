package updatehost

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
)

type customSupervisor struct {
	runner CommandRunner
	trust  func(string) error
}

func NewCustom(runner CommandRunner) Supervisor {
	return &customSupervisor{runner: runner, trust: trustedExecutable}
}
func (s *customSupervisor) run(ctx context.Context, in update.Instance, verb string) ([]byte, error) {
	if s.runner == nil || s.trust == nil || in.Supervisor.Kind != "custom" || !instanceID.MatchString(in.ID) || in.Supervisor.AdapterPath == "" {
		return nil, update.ErrSetupRequired
	}
	if err := s.trust(in.Supervisor.AdapterPath); err != nil {
		return nil, update.ErrSetupRequired
	}
	out, _, err := s.runner.Run(ctx, 0, 0, in.Supervisor.AdapterPath, []string{verb, in.ID}, nil)
	if len(out) > commandOutputLimit {
		return nil, update.ErrIncompatible
	}
	return out, err
}
func (s *customSupervisor) Probe(ctx context.Context, in update.Instance) (update.ServiceState, error) {
	var state update.ServiceState
	out, err := s.run(ctx, in, "probe")
	if err != nil {
		return state, err
	}
	d := json.NewDecoder(bytes.NewReader(out))
	d.DisallowUnknownFields()
	var wire struct {
		InstanceID *string `json:"instanceId"`
		Running    *bool   `json:"running"`
		Enabled    *bool   `json:"enabled"`
		Masked     *bool   `json:"masked"`
		Loaded     *bool   `json:"loaded"`
		PID        *int    `json:"pid"`
	}
	if d.Decode(&wire) != nil || wire.InstanceID == nil || wire.Running == nil || wire.Enabled == nil || wire.Masked == nil || wire.Loaded == nil || wire.PID == nil {
		return state, update.ErrUnavailable
	}
	state = update.ServiceState{InstanceID: *wire.InstanceID, Running: *wire.Running, Enabled: *wire.Enabled, Masked: *wire.Masked, Loaded: *wire.Loaded, PID: *wire.PID}
	var trailing json.RawMessage
	if d.Decode(&trailing) != io.EOF || state.InstanceID != in.ID || state.PID < 0 || state.Running && (!state.Loaded || state.PID == 0) || !state.Running && state.PID != 0 {
		return state, update.ErrUnavailable
	}
	return state, nil
}
func (s *customSupervisor) Stop(ctx context.Context, in update.Instance) error {
	_, err := s.run(ctx, in, "stop")
	return err
}
func (s *customSupervisor) Start(ctx context.Context, in update.Instance, state update.ServiceState) error {
	if state.InstanceID != in.ID {
		return update.ErrUnauthorized
	}
	if !state.Running || state.Masked || !state.Loaded {
		return nil
	}
	_, err := s.run(ctx, in, "start")
	return err
}
