//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// osResizes sends on the returned channel whenever the terminal is resized
// (SIGWINCH) until ctx ends.
func osResizes(ctx context.Context) <-chan struct{} {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	go func() {
		defer signal.Stop(sigs)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigs:
				select {
				case out <- struct{}{}:
				default: // a resize is already pending; the size is read when it is handled
				}
			}
		}
	}()
	return out
}
