package client

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePrefix = "github.com/rcss-labs/stackstead/internal/"

// The client must stay usable without pulling the server in: it may depend
// only on internal/apitypes among the module's internal packages.
func TestClientDependsOnNoServerInternals(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, modulePrefix) && pkg != modulePrefix+"apitypes" && pkg != modulePrefix+"client" {
			t.Errorf("internal/client depends on %s", pkg)
		}
	}
}
