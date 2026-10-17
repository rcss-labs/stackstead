package updateipc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/update"
)

type Client struct{ socket, installation string }

func NewClient(socket, installationID string) *Client { return &Client{socket, installationID} }

// Call authenticates the server before sending any application bytes.
func Call(ctx context.Context, socket string, uid uint32, q Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, Deadline)
	defer cancel()
	tr := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, e := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if e != nil {
			return nil, update.ErrUnavailable
		}
		actual, e := SocketUID(c)
		if e != nil || actual != uid {
			c.Close()
			return nil, update.ErrUnauthorized
		}
		return c, nil
	}}
	defer tr.CloseIdleConnections()
	b, e := json.Marshal(q)
	if e != nil {
		return Response{}, e
	}
	r, e := http.NewRequestWithContext(ctx, "POST", "http://local/rpc", bytes.NewReader(b))
	if e != nil {
		return Response{}, e
	}
	res, e := (&http.Client{Transport: tr}).Do(r)
	if e != nil {
		return Response{}, e
	}
	defer res.Body.Close()
	var v Response
	d := json.NewDecoder(io.LimitReader(res.Body, MaxRequest+1))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil {
		return v, update.ErrIncompatible
	}
	if e = d.Decode(new(struct{})); e != io.EOF {
		return v, update.ErrIncompatible
	}
	return v, DecodeError(v.Error)
}
func (c *Client) call(ctx context.Context, q Request) (Response, error) {
	q.InstallationID = c.installation
	return Call(ctx, c.socket, 0, q)
}
func (c *Client) Startup(ctx context.Context, in string) (*update.Probation, error) {
	v, e := c.call(ctx, Request{Operation: "startup", InstanceID: in})
	return v.Probation, e
}
func (c *Client) Status(ctx context.Context) (update.UpdateStatus, error) {
	v, e := c.call(ctx, Request{Operation: "status"})
	if e == nil && v.Status == nil {
		e = update.ErrIncompatible
	}
	if v.Status == nil {
		return update.UpdateStatus{}, e
	}
	return *v.Status, e
}
func (c *Client) Check(ctx context.Context) (update.UpdateStatus, error) {
	v, e := c.call(ctx, Request{Operation: "check"})
	if e == nil && v.Status == nil {
		e = update.ErrIncompatible
	}
	if v.Status == nil {
		return update.UpdateStatus{}, e
	}
	return *v.Status, e
}
func (c *Client) job(ctx context.Context, q Request) (update.JobView, error) {
	v, e := c.call(ctx, q)
	if e == nil && v.Job == nil {
		e = update.ErrIncompatible
	}
	if v.Job == nil {
		return update.JobView{}, e
	}
	return *v.Job, e
}
func (c *Client) Begin(ctx context.Context, q update.BeginRequest) (update.JobView, error) {
	return c.job(ctx, Request{Operation: "begin", Begin: &q})
}
func (c *Client) Job(ctx context.Context, id string) (update.JobView, error) {
	return c.job(ctx, Request{Operation: "job", JobID: id})
}
func (c *Client) Cancel(ctx context.Context, id string) (update.JobView, error) {
	return c.job(ctx, Request{Operation: "cancel", JobID: id})
}
