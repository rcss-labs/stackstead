//go:build installer

package installer_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

// The installer tests must never write into the developer's real home (a
// macOS real-mode test once did: a LaunchAgent and an Application Support
// tree appeared in the real ~/Library). TestMain snapshots the places
// install.sh writes to on macOS before the run and fails the whole run when
// anything there was created, changed or removed.

// realHomes returns the account's home directory and $HOME of the test
// process, without duplicates.
func realHomes() []string {
	var homes []string
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homes = append(homes, filepath.Clean(u.HomeDir))
	}
	if h := os.Getenv("HOME"); h != "" && (len(homes) == 0 || filepath.Clean(h) != homes[0]) {
		homes = append(homes, filepath.Clean(h))
	}
	return homes
}

// snapshotHomes records every path under <home>/Library/Application
// Support/Stackstead and <home>/Library/Logs/Stackstead and every
// <home>/Library/LaunchAgents entry naming stackstead, with type, mode, size
// and mtime; the watched roots are recorded as "absent" when missing. Only
// stackstead paths are watched, so other software writing to LaunchAgents
// during the run cannot fail it.
func snapshotHomes(homes []string) map[string]string {
	snap := map[string]string{}
	record := func(p string, fi fs.FileInfo) {
		snap[p] = fmt.Sprintf("%s size=%d mtime=%d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
	}
	for _, home := range homes {
		for _, root := range []string{
			filepath.Join(home, "Library", "Application Support", "Stackstead"),
			filepath.Join(home, "Library", "Logs", "Stackstead"),
		} {
			if _, err := os.Lstat(root); err != nil {
				snap[root] = "absent"
				continue
			}
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					snap[p] = "unreadable: " + err.Error()
					return nil
				}
				if fi, err := d.Info(); err == nil {
					record(p, fi)
				}
				return nil
			})
		}
		agents := filepath.Join(home, "Library", "LaunchAgents")
		entries, _ := os.ReadDir(agents)
		for _, e := range entries {
			if !strings.Contains(strings.ToLower(e.Name()), "stackstead") {
				continue
			}
			if fi, err := e.Info(); err == nil {
				record(filepath.Join(agents, e.Name()), fi)
			}
		}
	}
	return snap
}

// homeChanges lists what differs between two snapshots.
func homeChanges(before, after map[string]string) []string {
	var changes []string
	for p, a := range after {
		b, ok := before[p]
		switch {
		case !ok:
			changes = append(changes, "created: "+p+" ("+a+")")
		case a != b:
			changes = append(changes, "changed: "+p+" (was "+b+", now "+a+")")
		}
	}
	for p, b := range before {
		if _, ok := after[p]; !ok {
			changes = append(changes, "removed: "+p+" (was "+b+")")
		}
	}
	sort.Strings(changes)
	return changes
}

// guardRealHomes runs fn (the test run) between two snapshots of the real
// homes; on any change it reports every path loudly and returns exit code 1.
func guardRealHomes(fn func() int) int {
	homes := realHomes()
	before := snapshotHomes(homes)
	code := fn()
	if changes := homeChanges(before, snapshotHomes(homes)); len(changes) > 0 {
		fmt.Fprintf(os.Stderr, "\nFAIL: HERMETICITY: the installer tests wrote into the real home (%s):\n  %s\n"+
			"Some test resolved the developer's home instead of a temporary one; find it before running these tests again.\n",
			strings.Join(homes, ", "), strings.Join(changes, "\n  "))
		return 1
	}
	return code
}
