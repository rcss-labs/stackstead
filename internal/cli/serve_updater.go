package cli

import (
	"context"
	"errors"
	"net"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
)

type locatorStartup struct{ locator update.ClientLocator }

func (p locatorStartup) StartupProbation(ctx context.Context) (*update.Probation, error) {
	return updateipc.NewClient(p.locator.Socket, p.locator.InstallationID).Startup(ctx, p.locator.InstanceID)
}
func lookupUpdater(path string, uid uint32, read func(string, uint32) (update.ClientLocator, error)) (*update.ClientLocator, error) {
	if path == "" {
		return nil, nil
	}
	l, e := read(path, uid)
	if errors.Is(e, update.ErrLocatorAbsent) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !l.Enabled {
		return nil, nil
	}
	return &l, nil
}

var updaterLocatorReader = update.ReadClientLocator

func listenForReadiness(address string, ready func() error) (net.Listener, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if err = ready(); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
