package engine

import "testing"

func TestContainerHealth(t *testing.T) {
	tests := []struct {
		name, state, status, want string
	}{
		{"healthy", "running", "Up 5 days (healthy)", "healthy"},
		{"unhealthy", "running", "Up 1 minute (unhealthy)", "unhealthy"},
		{"starting", "running", "Up 3 seconds (health: starting)", "starting"},
		{"no healthcheck", "running", "Up 2 hours", "none"},
		{"exited", "exited", "Exited (0) 1 hour ago", "none"},
		{"not running ignores text", "restarting", "Up 1 second (healthy)", "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Container{State: tc.state, Status: tc.status}).Health(); got != tc.want {
				t.Errorf("Health() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContainerExitCode(t *testing.T) {
	tests := []struct {
		status string
		code   int
		ok     bool
	}{
		{"Exited (137) 2 minutes ago", 137, true},
		{"Exited (-1) 3 seconds ago", -1, true},
		{"Exited (0) 1 hour ago", 0, true},
		{"Up 2 hours", 0, false},
	}
	for _, tc := range tests {
		code, ok := (Container{Status: tc.status}).ExitCode()
		if code != tc.code || ok != tc.ok {
			t.Errorf("ExitCode(%q) = %d, %v; want %d, %v", tc.status, code, ok, tc.code, tc.ok)
		}
	}
}
