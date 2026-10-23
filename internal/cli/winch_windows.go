//go:build windows

package cli

import "context"

// osResizes has no resize signal to listen to on Windows; the terminal keeps
// the size it had when the session started.
func osResizes(context.Context) <-chan struct{} { return nil }
