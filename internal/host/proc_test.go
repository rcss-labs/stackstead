package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	procA   = "testdata/proc/a"
	procB   = "testdata/proc/b"
	sysTree = "testdata/sys"
)

func TestReadStat(t *testing.T) {
	cpu, cores, err := readStat(procA)
	if err != nil {
		t.Fatal(err)
	}
	if cores != 8 {
		t.Errorf("cores = %d, want 8", cores)
	}
	// cpu 1000 100 400 8000 500 0 0 0 0 0: total 10000, idle = 8000 + iowait 500.
	if cpu.total != 10000 || cpu.idle != 8500 {
		t.Errorf("cpu = %+v, want total 10000 idle 8500", cpu)
	}
}

func TestReadMeminfo(t *testing.T) {
	used, total, swapUsed, swapTotal, ok, err := readMeminfo(procA)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if total != 16384000*1024 || used != (16384000-4096000)*1024 {
		t.Errorf("used=%d total=%d", used, total)
	}
	if swapTotal != 2097152*1024 || swapUsed != (2097152-1048576)*1024 {
		t.Errorf("swapUsed=%d swapTotal=%d", swapUsed, swapTotal)
	}
}

func TestReadMeminfoWithoutMemAvailable(t *testing.T) {
	_, total, _, _, ok, err := readMeminfo("testdata/proc/nomemavail")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ok = true, want false when MemAvailable is missing")
	}
	if total != 16384000*1024 {
		t.Errorf("total = %d", total)
	}
}

func TestReadLoadavg(t *testing.T) {
	l1, l5, l15, err := readLoadavg(procA)
	if err != nil {
		t.Fatal(err)
	}
	if l1 != 0.52 || l5 != 0.48 || l15 != 0.40 {
		t.Errorf("load = %v %v %v", l1, l5, l15)
	}
}

func TestReadUptime(t *testing.T) {
	s, err := readUptime(procA)
	if err != nil {
		t.Fatal(err)
	}
	if s != 12345.67 {
		t.Errorf("uptime = %v", s)
	}
}

func TestReadNetDevPhysicalOnly(t *testing.T) {
	rx, tx, per, err := readNetDev(procA, sysTree)
	if err != nil {
		t.Fatal(err)
	}
	if rx != 10000000 || tx != 4000000 {
		t.Errorf("rx=%d tx=%d, want eth0 only", rx, tx)
	}
	if len(per) != 1 || per["eth0"] != [2]uint64{10000000, 4000000} {
		t.Errorf("perIface = %v", per)
	}
}

func TestReadNetDevFallbackExcludesVirtual(t *testing.T) {
	rx, tx, per, err := readNetDev(procA, "testdata/sys-nophysical")
	if err != nil {
		t.Fatal(err)
	}
	// eth0 + wg0 remain; lo, docker0, br-*, veth* are excluded.
	if rx != 10000000+60000 || tx != 4000000+40000 {
		t.Errorf("rx=%d tx=%d", rx, tx)
	}
	for _, name := range []string{"lo", "docker0", "br-1a2b", "veth9f1"} {
		if _, found := per[name]; found {
			t.Errorf("%s should be excluded", name)
		}
	}
	if _, found := per["wg0"]; !found {
		t.Error("wg0 should be included")
	}
}

func TestReadDiskstatsWholeDisksOnly(t *testing.T) {
	r, w, err := readDiskstats(procA)
	if err != nil {
		t.Fatal(err)
	}
	// sda 2000/1000 sectors + nvme0n1 4000/3000 sectors, x512.
	if r != 6000*512 || w != 4000*512 {
		t.Errorf("read=%d write=%d", r, w)
	}
}

func TestParserErrorsNameFile(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu  1 x 3 4 5\ncpu0 1 2 3 4 5\n")
	write("meminfo", "MemTotal: abc kB\nMemAvailable: 1 kB\n")
	write("loadavg", "a b c 1/2 3\n")
	write("uptime", "zzz 1\n")
	write("net/dev", "h1\nh2\n  eth0: bad 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n")
	write("diskstats", "8 0 sda 1 2 xx 4 5 6 7 8 9 10 11\n")

	_, _, errStat := readStat(dir)
	_, _, _, _, _, errMem := readMeminfo(dir)
	_, _, _, errLoad := readLoadavg(dir)
	_, errUp := readUptime(dir)
	_, _, _, errNet := readNetDev(dir, sysTree)
	_, _, errDisk := readDiskstats(dir)
	cases := map[string]error{
		"stat": errStat, "meminfo": errMem, "loadavg": errLoad,
		"uptime": errUp, "net/dev": errNet, "diskstats": errDisk,
	}
	for name, err := range cases {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error %v does not name the file", name, err)
		}
	}

	if _, _, err := readStat(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "stat") {
		t.Errorf("missing file error = %v", err)
	}
}
