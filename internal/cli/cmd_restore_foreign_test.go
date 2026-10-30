package cli

import (
	"testing"

	"github.com/rcss-labs/stackstead/internal/backup"
)

func TestRestoreDefaultsNoStartForForeignArchive(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	calls.foreign = true
	env := newTestEnv(t, nil)
	wantCode(t, runRestore(env, "/tmp/a.tar.gz", "--yes", "--config", h.cfgPath), ExitOK)
	if calls.restored.Kind == "" || calls.started {
		t.Errorf("restored = %+v, started = %v; a foreign archive must not start stacks by default", calls.restored, calls.started)
	}

	own := fakeRestoreSeams(t, nil)
	wantCode(t, runRestore(newTestEnv(t, nil), "/tmp/a.tar.gz", "--yes", "--config", h.cfgPath), ExitOK)
	if !own.started {
		t.Error("an archive from this host stopped starting stacks by default")
	}
}

func TestRestoreStartFlagOverridesForeignDefault(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	calls.foreign = true
	wantCode(t, runRestore(newTestEnv(t, nil), "/tmp/a.tar.gz", "--yes", "--start", "--config", h.cfgPath), ExitOK)
	if !calls.started {
		t.Error("--start did not start the stacks of a foreign archive")
	}
}

func TestRestoreRejectsStartAndNoStart(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	wantCode(t, runRestore(newTestEnv(t, nil), "/tmp/a.tar.gz", "--yes", "--start", "--no-start", "--config", h.cfgPath), ExitUsage)
	if len(calls.inspections) != 0 || calls.restored.Kind != "" {
		t.Errorf("the archive was opened despite the contradictory flags: %+v", calls)
	}
}

func TestRestoreSummaryListsRisksAndOrigin(t *testing.T) {
	h := newLocalHost(t, true)
	calls := fakeRestoreSeams(t, nil)
	calls.foreign = true
	calls.instanceID = "0123456789abcdef0123"
	calls.risks = []backup.ComposeRisk{
		{Stack: "web", File: "compose.yaml", Service: "app", Kind: "privileged", Detail: "true"},
		{Stack: "web", File: "compose.yaml", Service: "app", Kind: "host_bind", Detail: "/:/host"},
	}
	env := newTestEnv(t, nil)
	wantCode(t, runRestore(env, "/tmp/a.tar.gz", "--yes", "--start", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(),
		"origin     another host (instance 01234567); stacks stay stopped unless --start",
		"risks",
		"web/app  privileged  true",
		"web/app  host_bind  /:/host",
		"warning: starting stacks whose compose files have the settings above")

	calls = fakeRestoreSeams(t, nil)
	env = newTestEnv(t, nil)
	wantCode(t, runRestore(env, "/tmp/a.tar.gz", "--yes", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "origin     this host", "risks      none")
}
