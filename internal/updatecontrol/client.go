package updatecontrol

import (
	"context"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
)

type Client struct{}

func (c *Client) call(ctx context.Context, in update.Instance, q updateipc.Request) (updateipc.Response, error) {
	q.InstanceID = in.ID
	return updateipc.Call(ctx, in.ControlSocket, in.UID, q)
}
func (c *Client) Freeze(ctx context.Context, in update.Instance) error {
	_, e := c.call(ctx, in, updateipc.Request{Operation: "freeze"})
	return e
}
func (c *Client) Prepare(ctx context.Context, in update.Instance, p update.Probation) error {
	_, e := c.call(ctx, in, updateipc.Request{Operation: "prepare", Probation: &p})
	return e
}
func (c *Client) Ready(ctx context.Context, in update.Instance) (update.Readiness, error) {
	v, e := c.call(ctx, in, updateipc.Request{Operation: "ready"})
	if e == nil && v.Readiness == nil {
		e = update.ErrIncompatible
	}
	if v.Readiness == nil {
		return update.Readiness{}, e
	}
	return *v.Readiness, e
}
func (c *Client) Release(ctx context.Context, in update.Instance, job string) error {
	_, e := c.call(ctx, in, updateipc.Request{Operation: "release", JobID: job})
	return e
}
