// Package sysinfo reads live, container-scoped resource usage from cgroups v2
// and /proc, and derives per-second rates from consecutive samples for the
// System Dashboard's SSE stream (see internal/api/sysinfo.go).
//
// Everything here is deliberately pure stdlib: the production deployment is a
// headless container, and the whole point is to read the container's OWN
// cgroup v2 + /proc counters directly rather than depend on a metrics agent.
// Sample() reads cumulative counters; ComputeRates() turns two samples into
// the apidto.SysinfoSnapshot the frontend renders.
//
// UNVERIFIED ASSUMPTION: this assumes a cgroups v2 unified hierarchy mounted
// at /sys/fs/cgroup with the container's own leaf files exposed at the root of
// that mount (cpu.stat / memory.current / memory.max / io.stat) — the normal
// shape for a container running under a modern Docker/containerd on a v2 host.
// On a cgroups v1 host, or a v2 host where these files live at a nested leaf
// rather than the mount root, Sample() returns an error and the stream emits
// an SSE error event rather than crashing.
package sysinfo

import (
	"bufio"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// MountSpec is a named filesystem path to measure via statfs.
// An empty Path means the mount is not configured.
type MountSpec struct {
	Name string
	Path string
}

// StorageEntry is one resolved statfs reading for a named mount.
type StorageEntry struct {
	Name       string
	TotalBytes int64
	AvailBytes int64
	Configured bool // false when Path was empty or statfs failed
}

// RawSample is one reading of the cumulative counters. Rates are derived later
// by ComputeRates from two of these; a single RawSample on its own is not
// directly meaningful for anything but memory (which is already a level, not a
// counter).
type RawSample struct {
	CapturedAt          time.Time
	CPUUsageMicros      int64 // from /sys/fs/cgroup/cpu.stat usage_usec
	MemUsedBytes        int64 // from /sys/fs/cgroup/memory.current
	MemLimitBytes       int64 // from /sys/fs/cgroup/memory.max; -1 = unlimited
	NetRxBytes          int64 // sum across non-loopback interfaces from /proc/net/dev
	NetTxBytes          int64
	ContainerDiskRBytes int64 // sum rbytes from /sys/fs/cgroup/io.stat
	ContainerDiskWBytes int64 // sum wbytes from /sys/fs/cgroup/io.stat
	ServerDisks         []DiskRaw
	StorageMounts       []StorageEntry // one statfs reading per named mount
	GPUs                []GPURaw       // point-in-time per-GPU reading, no delta needed
}

// DiskRaw is one physical disk's cumulative read/write bytes from
// /proc/diskstats (sectors * 512).
type DiskRaw struct {
	Name   string
	RBytes int64 // sectors read * 512
	WBytes int64 // sectors written * 512
}

// sysinfoPathConfig is the set of cgroup/proc paths Sample reads. Split out so
// sampleFromPaths can be pointed at temp fixtures in tests; production always
// uses defaultPaths.
type sysinfoPathConfig struct {
	cpuStat    string
	memCurrent string
	memMax     string
	netDev     string
	ioStat     string
	diskstats  string
	// gpuDrmBasePath is the /sys/class/drm base whose card* dirs readGPUs
	// enumerates. Unlike the counters above, a GPU read failure is soft (never
	// aborts the sample), so it needs no error path in sampleFromPaths.
	gpuDrmBasePath string
}

// defaultPaths are the real cgroups v2 / proc locations.
// UNVERIFIED ASSUMPTION: cgroups v2 unified hierarchy at /sys/fs/cgroup with
// the container's own leaf files at the mount root (see package doc).
var defaultPaths = sysinfoPathConfig{
	cpuStat:        "/sys/fs/cgroup/cpu.stat",
	memCurrent:     "/sys/fs/cgroup/memory.current",
	memMax:         "/sys/fs/cgroup/memory.max",
	netDev:         "/proc/net/dev",
	ioStat:         "/sys/fs/cgroup/io.stat",
	diskstats:      "/proc/diskstats",
	gpuDrmBasePath: "/sys/class/drm",
}

// physicalDiskRe matches whole physical block-device names in /proc/diskstats,
// anchored end-to-end so partitions are excluded: it matches e.g. sda, nvme0n1,
// mmcblk0 but rejects sda1, nvme0n1p1, mmcblk0p1 (a partition is a slice of a
// disk, not a disk — the dashboard reports per-disk I/O, not per-partition).
var physicalDiskRe = regexp.MustCompile(`^(sd[a-z]+|hd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`)

// bytesPerSector is the conventional /proc/diskstats sector size (always 512,
// independent of the device's physical/logical sector size).
const bytesPerSector = 512

// Sample reads the current raw cumulative values from the real cgroup/proc
// files. Returns an error if any required file is unreadable or unparseable.
func Sample(mounts []MountSpec) (RawSample, error) {
	return sampleFromPaths(defaultPaths, mounts)
}

// sampleFromPaths is the testable core: it reads every counter from the given
// paths so tests can point it at temp fixtures. Any single unreadable/
// unparseable required file fails the whole sample — a partial sample would
// silently under-report a rate.
func sampleFromPaths(paths sysinfoPathConfig, mounts []MountSpec) (RawSample, error) {
	s := RawSample{CapturedAt: time.Now()}

	cpu, err := readCPUUsageMicros(paths.cpuStat)
	if err != nil {
		return RawSample{}, err
	}
	s.CPUUsageMicros = cpu

	memUsed, err := readInt64File(paths.memCurrent)
	if err != nil {
		return RawSample{}, err
	}
	s.MemUsedBytes = memUsed

	memLimit, err := readMemMax(paths.memMax)
	if err != nil {
		return RawSample{}, err
	}
	s.MemLimitBytes = memLimit

	rx, tx, err := readNetDev(paths.netDev)
	if err != nil {
		return RawSample{}, err
	}
	s.NetRxBytes = rx
	s.NetTxBytes = tx

	rBytes, wBytes, err := readIOStat(paths.ioStat)
	if err != nil {
		return RawSample{}, err
	}
	s.ContainerDiskRBytes = rBytes
	s.ContainerDiskWBytes = wBytes

	disks, err := readDiskstats(paths.diskstats)
	if err != nil {
		return RawSample{}, err
	}
	s.ServerDisks = disks

	entries := make([]StorageEntry, 0, len(mounts))
	for _, m := range mounts {
		if m.Path == "" {
			entries = append(entries, StorageEntry{Name: m.Name, Configured: false})
			continue
		}
		total, avail, err := readStorageUsage(m.Path)
		if err != nil {
			entries = append(entries, StorageEntry{Name: m.Name, Configured: false})
			continue
		}
		entries = append(entries, StorageEntry{Name: m.Name, TotalBytes: total, AvailBytes: avail, Configured: true})
	}
	s.StorageMounts = entries

	// GPUs are a soft read: a missing card, unreadable file, or unknown vendor
	// yields a shorter/empty slice, never an error — a GPU failure must not
	// blank out the CPU/RAM/disk metrics on the dashboard.
	s.GPUs = readGPUs(paths.gpuDrmBasePath)

	return s, nil
}

// readStorageUsage reports the total and available bytes of the filesystem
// backing path via statfs. Point-in-time (a level, not a counter), so it's
// read straight into the snapshot rather than differenced by ComputeRates.
func readStorageUsage(path string) (totalBytes, availBytes int64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return
	}
	totalBytes = int64(stat.Blocks) * stat.Frsize
	availBytes = int64(stat.Bavail) * stat.Frsize
	return
}

// readCPUUsageMicros parses /sys/fs/cgroup/cpu.stat and returns usage_usec.
// Lines are "key value" space-separated; we want the "usage_usec" line.
func readCPUUsageMicros(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, &parseError{path: path, what: "usage_usec not found"}
}

// readInt64File reads a file containing a single int64 (memory.current).
func readInt64File(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

// readMemMax reads memory.max, which is either an int64 or the literal "max"
// (unlimited) → -1.
func readMemMax(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	v := strings.TrimSpace(string(data))
	if v == "max" {
		return -1, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

// readNetDev parses /proc/net/dev, summing rx_bytes (field 1 after the name)
// and tx_bytes (field 9 after the name) across all interfaces except lo.
// The two header lines are skipped by requiring a colon in the name field.
func readNetDev(path string) (rx, tx int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Split on the first colon so a large rx value abutting the colon
		// (e.g. "eth0:12345") still separates cleanly. Header lines have no
		// colon and are skipped.
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		// After the colon: rx_bytes is field 0, tx_bytes is field 8
		// (rx has 8 columns before tx begins).
		if len(fields) < 9 {
			continue
		}
		rxb, e := strconv.ParseInt(fields[0], 10, 64)
		if e != nil {
			return 0, 0, e
		}
		txb, e := strconv.ParseInt(fields[8], 10, 64)
		if e != nil {
			return 0, 0, e
		}
		rx += rxb
		tx += txb
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return rx, tx, nil
}

// readIOStat parses /sys/fs/cgroup/io.stat, summing rbytes= and wbytes= across
// every device entry. Lines look like "8:0 rbytes=123 wbytes=456 ...".
func readIOStat(path string) (rBytes, wBytes int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		for _, tok := range strings.Fields(scanner.Text()) {
			switch {
			case strings.HasPrefix(tok, "rbytes="):
				v, e := strconv.ParseInt(strings.TrimPrefix(tok, "rbytes="), 10, 64)
				if e != nil {
					return 0, 0, e
				}
				rBytes += v
			case strings.HasPrefix(tok, "wbytes="):
				v, e := strconv.ParseInt(strings.TrimPrefix(tok, "wbytes="), 10, 64)
				if e != nil {
					return 0, 0, e
				}
				wBytes += v
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return rBytes, wBytes, nil
}

// readDiskstats parses /proc/diskstats, returning one DiskRaw per physical
// device (name matching physicalDiskRe). Fields are space-separated:
// MAJ MIN NAME reads_completed reads_merged sectors_read ms_read
// writes_completed writes_merged sectors_written ...
// RBytes = sectors_read*512, WBytes = sectors_written*512.
func readDiskstats(path string) ([]DiskRaw, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var disks []DiskRaw
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !physicalDiskRe.MatchString(name) {
			continue
		}
		sectorsRead, e := strconv.ParseInt(fields[5], 10, 64)
		if e != nil {
			return nil, e
		}
		sectorsWritten, e := strconv.ParseInt(fields[9], 10, 64)
		if e != nil {
			return nil, e
		}
		disks = append(disks, DiskRaw{
			Name:   name,
			RBytes: sectorsRead * bytesPerSector,
			WBytes: sectorsWritten * bytesPerSector,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return disks, nil
}

// effectiveCPUCount returns the number of CPU cores available to this process,
// reading the cgroup v2 cpu.max quota when running in a CPU-limited container.
// Falls back to runtime.NumCPU() when the file is absent, unreadable, or the
// quota is "max" (unlimited). This corrects the case where runtime.NumCPU()
// returns the host's full core count inside a container with a CPU quota set —
// without this, reported CPU% reads artificially low in a limited container.
func effectiveCPUCount() float64 {
	data, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return float64(runtime.NumCPU())
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) != 2 || fields[0] == "max" {
		return float64(runtime.NumCPU())
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || period == 0 {
		return float64(runtime.NumCPU())
	}
	cores := quota / period
	if cores < 1 {
		cores = 1
	}
	return cores
}

// ComputeRates computes per-second rates from two consecutive samples.
// CPU %: (delta_usage_usec / elapsed_sec / 1_000_000 / numCPU) * 100, clamped
// to [0,100]. bytes/sec fields are delta/elapsed. If elapsed <= 0 a 1s
// fallback avoids division by zero. Server disks are matched by name across
// the two samples; a disk present in curr but not prev (or vice-versa) yields
// a 0 rate for that interval rather than a spurious spike.
func ComputeRates(prev, curr RawSample) apidto.SysinfoSnapshot {
	elapsed := curr.CapturedAt.Sub(prev.CapturedAt).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	numCPU := effectiveCPUCount()
	// Convert to float BEFORE dividing — an int delta / int numCPU would
	// truncate to zero for any sub-100%-per-core reading.
	cpuPercent := float64(curr.CPUUsageMicros-prev.CPUUsageMicros) / elapsed / 1_000_000 / numCPU * 100
	if cpuPercent < 0 {
		cpuPercent = 0
	}
	if cpuPercent > 100 {
		cpuPercent = 100
	}

	mounts := make([]apidto.SysinfoStorageMount, len(curr.StorageMounts))
	for i, e := range curr.StorageMounts {
		mounts[i] = apidto.SysinfoStorageMount{
			Name:       e.Name,
			TotalBytes: e.TotalBytes,
			AvailBytes: e.AvailBytes,
			Configured: e.Configured,
		}
	}

	// GPUs are point-in-time levels (util/VRAM/power), read straight from the
	// current sample — no delta pass, unlike the counters above.
	var gpus []apidto.SysinfoGPU
	for _, g := range curr.GPUs {
		gpus = append(gpus, apidto.SysinfoGPU{
			Name:            g.Name,
			UtilPercent:     g.UtilPercent,
			VRAMUsedBytes:   g.VRAMUsedBytes,
			VRAMTotalBytes:  g.VRAMTotalBytes,
			PowerMicrowatts: g.PowerMicrowatts,
		})
	}

	snap := apidto.SysinfoSnapshot{
		CPUPercent:            cpuPercent,
		MemUsedBytes:          curr.MemUsedBytes,
		MemLimitBytes:         curr.MemLimitBytes,
		NetRxBPS:              perSecond(prev.NetRxBytes, curr.NetRxBytes, elapsed),
		NetTxBPS:              perSecond(prev.NetTxBytes, curr.NetTxBytes, elapsed),
		ContainerDiskReadBPS:  perSecond(prev.ContainerDiskRBytes, curr.ContainerDiskRBytes, elapsed),
		ContainerDiskWriteBPS: perSecond(prev.ContainerDiskWBytes, curr.ContainerDiskWBytes, elapsed),
		StorageMounts:         mounts,
		GPUs:                  gpus,
	}

	prevByName := make(map[string]DiskRaw, len(prev.ServerDisks))
	for _, d := range prev.ServerDisks {
		prevByName[d.Name] = d
	}
	for _, cd := range curr.ServerDisks {
		pd := prevByName[cd.Name] // zero value if unseen → 0 rate
		snap.ServerDisks = append(snap.ServerDisks, apidto.SysinfoServerDisk{
			Name:     cd.Name,
			ReadBPS:  perSecond(pd.RBytes, cd.RBytes, elapsed),
			WriteBPS: perSecond(pd.WBytes, cd.WBytes, elapsed),
		})
	}
	return snap
}

// perSecond turns a cumulative-counter delta into a per-second rate, clamping
// a negative delta (counter reset / disk reappearing) to 0.
func perSecond(prev, curr int64, elapsedSec float64) float64 {
	delta := curr - prev
	if delta < 0 {
		return 0
	}
	return float64(delta) / elapsedSec
}

// parseError is a small typed error for "file read but expected field absent".
type parseError struct {
	path string
	what string
}

func (e *parseError) Error() string { return e.path + ": " + e.what }
