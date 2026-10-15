// Package host reads Linux /proc counters and derives rates from successive
// readings. Everything here is stdlib-only and parses plain files so it
// compiles and is testable on every platform.
package host

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cpuTimes is the aggregate jiffy counters from the first line of /proc/stat.
// total sums user, nice, system, idle, iowait, irq, softirq and steal only:
// guest and guest_nice are already included in user and nice by the kernel.
type cpuTimes struct{ total, idle uint64 } // idle includes iowait

// reading is one snapshot of the cumulative counters.
type reading struct {
	at                  time.Time
	cpu                 cpuTimes
	cores               int
	netRx, netTx        uint64
	netByIface          map[string][2]uint64
	diskRead, diskWrite uint64 // bytes (sectors x 512)
}

const sectorSize = 512

var wholeDisk = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`)

func parseUint(file, what, s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid %s %q", file, what, s)
	}
	return v, nil
}

func readProc(procRoot, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, name))
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(b), nil
}

// readStat returns the aggregate cpu counters and the number of cpuN lines.
func readStat(procRoot string) (cpuTimes, int, error) {
	const file = "stat"
	text, err := readProc(procRoot, file)
	if err != nil {
		return cpuTimes{}, 0, err
	}
	var cpu cpuTimes
	var found bool
	cores := 0
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if f[0] == "cpu" {
			if len(f) < 5 {
				return cpuTimes{}, 0, fmt.Errorf("%s: short cpu line", file)
			}
			var total uint64
			for i, field := range f[1:min(len(f), 9)] {
				v, err := parseUint(file, "cpu field", field)
				if err != nil {
					return cpuTimes{}, 0, err
				}
				total += v
				if i == 3 || i == 4 { // idle, iowait
					cpu.idle += v
				}
			}
			cpu.total = total
			found = true
			continue
		}
		if _, err := strconv.Atoi(f[0][3:]); err == nil {
			cores++
		}
	}
	if !found {
		return cpuTimes{}, 0, fmt.Errorf("%s: no cpu line", file)
	}
	return cpu, cores, nil
}

// readMeminfo returns used/total memory and swap in bytes. ok is false when
// MemAvailable is missing (kernels before 3.14), so used is not meaningful.
func readMeminfo(procRoot string) (used, total, swapUsed, swapTotal uint64, ok bool, err error) {
	const file = "meminfo"
	text, err := readProc(procRoot, file)
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	vals := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		key := strings.TrimSuffix(f[0], ":")
		switch key {
		case "MemTotal", "MemAvailable", "SwapTotal", "SwapFree":
			v, err := parseUint(file, key, f[1])
			if err != nil {
				return 0, 0, 0, 0, false, err
			}
			vals[key] = v * 1024
		}
	}
	total, swapTotal = vals["MemTotal"], vals["SwapTotal"]
	if swapTotal >= vals["SwapFree"] {
		swapUsed = swapTotal - vals["SwapFree"]
	}
	avail, has := vals["MemAvailable"]
	if !has {
		return 0, total, swapUsed, swapTotal, false, nil
	}
	if total >= avail {
		used = total - avail
	}
	return used, total, swapUsed, swapTotal, true, nil
}

func readLoadavg(procRoot string) (l1, l5, l15 float64, err error) {
	const file = "loadavg"
	text, err := readProc(procRoot, file)
	if err != nil {
		return 0, 0, 0, err
	}
	f := strings.Fields(text)
	if len(f) < 3 {
		return 0, 0, 0, fmt.Errorf("%s: expected 3 load averages", file)
	}
	out := [3]float64{}
	for i := range out {
		if out[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return 0, 0, 0, fmt.Errorf("%s: invalid load %q", file, f[i])
		}
	}
	return out[0], out[1], out[2], nil
}

func readUptime(procRoot string) (seconds float64, err error) {
	const file = "uptime"
	text, err := readProc(procRoot, file)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(text)
	if len(f) < 1 {
		return 0, fmt.Errorf("%s: empty file", file)
	}
	if seconds, err = strconv.ParseFloat(f[0], 64); err != nil {
		return 0, fmt.Errorf("%s: invalid uptime %q", file, f[0])
	}
	return seconds, nil
}

// readNetDev sums rx/tx bytes over physical interfaces (those with a device
// entry under sysRoot/class/net). With none present it falls back to every
// interface except lo, docker*, br-* and veth*.
func readNetDev(procRoot, sysRoot string) (rx, tx uint64, perIface map[string][2]uint64, err error) {
	const file = "net/dev"
	text, err := readProc(procRoot, file)
	if err != nil {
		return 0, 0, nil, err
	}
	all := map[string][2]uint64{}
	for i, line := range strings.Split(text, "\n") {
		if i < 2 { // two header lines
			continue
		}
		name, rest, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return 0, 0, nil, fmt.Errorf("%s: short line for %s", file, name)
		}
		r, err := parseUint(file, name+" rx bytes", f[0])
		if err != nil {
			return 0, 0, nil, err
		}
		t, err := parseUint(file, name+" tx bytes", f[8])
		if err != nil {
			return 0, 0, nil, err
		}
		all[name] = [2]uint64{r, t}
	}

	perIface = map[string][2]uint64{}
	for name, v := range all {
		if _, statErr := os.Stat(filepath.Join(sysRoot, "class", "net", name, "device")); statErr == nil {
			perIface[name] = v
		}
	}
	if len(perIface) == 0 {
		for name, v := range all {
			if name == "lo" || strings.HasPrefix(name, "docker") ||
				strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth") {
				continue
			}
			perIface[name] = v
		}
	}
	for _, v := range perIface {
		rx += v[0]
		tx += v[1]
	}
	return rx, tx, perIface, nil
}

// readDiskstats sums sectors read/written (x512) over whole disks only, so
// partitions, loop, ram, zram, dm- and md devices are not double counted.
func readDiskstats(procRoot string) (readBytes, writeBytes uint64, err error) {
	const file = "diskstats"
	text, err := readProc(procRoot, file)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !wholeDisk.MatchString(f[2]) {
			continue
		}
		if len(f) < 10 {
			return 0, 0, fmt.Errorf("%s: short line for %s", file, f[2])
		}
		r, err := parseUint(file, f[2]+" sectors read", f[5])
		if err != nil {
			return 0, 0, err
		}
		w, err := parseUint(file, f[2]+" sectors written", f[9])
		if err != nil {
			return 0, 0, err
		}
		readBytes += r * sectorSize
		writeBytes += w * sectorSize
	}
	return readBytes, writeBytes, nil
}
