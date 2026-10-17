package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rcss-labs/stackstead/internal/updatehost"
)

// Task 13E supplies the public worker dispatch. Internal children already use the
// final fixed verb and accept their trusted operation solely through inherited FD3.
func main() {
	if len(os.Args) == 2 && os.Args[1] == "internal-child" {
		if updatehost.RunInternalChild(context.Background()) == nil {
			return
		}
		fmt.Fprintln(os.Stderr, "updater child refused operation")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "updater command unavailable")
	os.Exit(1)
}
