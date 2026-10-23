package client

import (
	"context"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestContainersList(t *testing.T) {
	c, got := newTestClient(t, jsonReply(200, []apitypes.ContainerRow{{ID: "0123456789ab", Name: "web"}}))
	rows, err := c.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" || got.path != "/api/containers" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if len(rows) != 1 || rows[0].Name != "web" {
		t.Errorf("rows = %+v", rows)
	}
	if c.Config().Session != "sess123" {
		t.Errorf("Config().Session = %q", c.Config().Session)
	}
}
