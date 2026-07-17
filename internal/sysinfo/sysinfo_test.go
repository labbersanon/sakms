package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFile writes content to a named file in dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// fixturePaths writes all six fixture files into a temp dir and returns the
// path config pointing at them. Callers override individual files' content.
func fixturePaths(t *testing.T, cpuStat, memCurrent, memMax, netDev, ioStat, diskstats string) sysinfoPathConfig {
	t.Helper()
	dir := t.TempDir()
	return sysinfoPathConfig{
		cpuStat:    writeFile(t, dir, "cpu.stat", cpuStat),
		memCurrent: writeFile(t, dir, "memory.current", memCurrent),
		memMax:     writeFile(t, dir, "memory.max", memMax),
		netDev:     writeFile(t, dir, "net_dev", netDev),
		ioStat:     writeFile(t, dir, "io.stat", ioStat),
		diskstats:  writeFile(t, dir, "diskstats", diskstats),
	}
}

// baseline fixture contents reused across tests; individual tests swap one.
const (
	cpuStatFixture    = "usage_usec 1000000\nuser_usec 600000\nsystem_usec 400000\n"
	memCurrentFixture = "104857600\n"
	memMaxFixture     = "209715200\n"
	netDevFixture     = "Inter-|   Receive                     |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 5000 10 0 0 0 0 0 0 5000 10 0 0 0 0 0 0\n" +
		"  eth0: 1000 20 0 0 0 0 0 0 2000 30 0 0 0 0 0 0\n"
	ioStatFixture    = "8:0 rbytes=1000 wbytes=2000 rios=5 wios=6 dbytes=0 dios=0\n8:16 rbytes=500 wbytes=1500 rios=3 wios=4 dbytes=0 dios=0\n"
	diskstatsFixture = " 259 0 nvme0n1 100 0 200 0 0 0 400 0 0 0 0 0 0 0 0 0 0\n" +
		" 259 1 nvme0n1p1 50 0 100 0 0 0 200 0 0 0 0 0 0 0 0 0 0\n" +
		"   7 0 loop0 1 0 2 0 0 0 4 0 0 0 0 0 0 0 0 0 0\n" +
		" 253 0 dm-0 1 0 2 0 0 0 4 0 0 0 0 0 0 0 0 0 0\n" +
		"   8 0 sda 10 0 20 0 0 0 40 0 0 0 0 0 0 0 0 0 0\n" +
		"   8 1 sda1 5 0 10 0 0 0 20 0 0 0 0 0 0 0 0 0 0\n"
)

func TestSampleCPU(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if s.CPUUsageMicros != 1_000_000 {
		t.Errorf("CPUUsageMicros = %d, want 1000000", s.CPUUsageMicros)
	}
}

func TestSampleMem_WithLimit(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if s.MemUsedBytes != 104857600 {
		t.Errorf("MemUsedBytes = %d, want 104857600", s.MemUsedBytes)
	}
	if s.MemLimitBytes != 209715200 {
		t.Errorf("MemLimitBytes = %d, want 209715200", s.MemLimitBytes)
	}
}

func TestSampleMem_Unlimited(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, "max\n", netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if s.MemLimitBytes != -1 {
		t.Errorf("MemLimitBytes = %d, want -1 (unlimited)", s.MemLimitBytes)
	}
}

func TestSampleNet(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	// lo (rx 5000 / tx 5000) must be skipped; only eth0 (rx 1000 / tx 2000).
	if s.NetRxBytes != 1000 {
		t.Errorf("NetRxBytes = %d, want 1000 (lo skipped)", s.NetRxBytes)
	}
	if s.NetTxBytes != 2000 {
		t.Errorf("NetTxBytes = %d, want 2000 (lo skipped)", s.NetTxBytes)
	}
}

// TestSampleNet_ColonAbutsValue verifies the split-on-first-colon handling
// when a large rx value has no space after the interface's colon.
func TestSampleNet_ColonAbutsValue(t *testing.T) {
	netDev := "Inter-|   Receive |  Transmit\n" +
		" face |bytes ...|bytes ...\n" +
		"eth0:99999999999 20 0 0 0 0 0 0 12345 30 0 0 0 0 0 0\n"
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDev, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if s.NetRxBytes != 99999999999 {
		t.Errorf("NetRxBytes = %d, want 99999999999 (colon abutting value)", s.NetRxBytes)
	}
	if s.NetTxBytes != 12345 {
		t.Errorf("NetTxBytes = %d, want 12345", s.NetTxBytes)
	}
}

func TestSampleDisk_Container(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	// rbytes 1000+500=1500, wbytes 2000+1500=3500.
	if s.ContainerDiskRBytes != 1500 {
		t.Errorf("ContainerDiskRBytes = %d, want 1500", s.ContainerDiskRBytes)
	}
	if s.ContainerDiskWBytes != 3500 {
		t.Errorf("ContainerDiskWBytes = %d, want 3500", s.ContainerDiskWBytes)
	}
}

func TestSampleDisk_Server(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, nil)
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	// nvme0n1 (whole disk) + sda (whole disk); loop0, dm-0, and the
	// partitions nvme0n1p1 / sda1 are all skipped.
	if len(s.ServerDisks) != 2 {
		t.Fatalf("ServerDisks len = %d, want 2 (nvme0n1, sda); got %+v", len(s.ServerDisks), s.ServerDisks)
	}
	byName := map[string]DiskRaw{}
	for _, d := range s.ServerDisks {
		byName[d.Name] = d
	}
	nvme, ok := byName["nvme0n1"]
	if !ok {
		t.Fatal("nvme0n1 not found in ServerDisks")
	}
	// sectors_read=200, sectors_written=400 → *512.
	if nvme.RBytes != 200*512 {
		t.Errorf("nvme0n1 RBytes = %d, want %d", nvme.RBytes, 200*512)
	}
	if nvme.WBytes != 400*512 {
		t.Errorf("nvme0n1 WBytes = %d, want %d", nvme.WBytes, 400*512)
	}
	sda, ok := byName["sda"]
	if !ok {
		t.Fatal("sda not found in ServerDisks")
	}
	if sda.RBytes != 20*512 {
		t.Errorf("sda RBytes = %d, want %d", sda.RBytes, 20*512)
	}
	if _, bad := byName["loop0"]; bad {
		t.Error("loop0 must be filtered out")
	}
	if _, bad := byName["dm-0"]; bad {
		t.Error("dm-0 must be filtered out")
	}
	if _, bad := byName["nvme0n1p1"]; bad {
		t.Error("nvme0n1p1 is a partition and must be filtered out")
	}
	if _, bad := byName["sda1"]; bad {
		t.Error("sda1 is a partition and must be filtered out")
	}
}

// TestSampleStorage verifies a configured mount (a real, stat-able filesystem
// path — the test's own temp dir) yields a Configured entry with sane totals.
func TestSampleStorage(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	dir := t.TempDir()
	writeFile(t, dir, "dummy", "x") // ensure the fs is populated/stat-able
	s, err := sampleFromPaths(paths, []MountSpec{{Name: "tmp", Path: dir}})
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if len(s.StorageMounts) != 1 {
		t.Fatalf("StorageMounts len = %d, want 1", len(s.StorageMounts))
	}
	m := s.StorageMounts[0]
	if !m.Configured {
		t.Errorf("StorageMounts[0].Configured = false, want true")
	}
	if m.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", m.TotalBytes)
	}
	if m.AvailBytes <= 0 {
		t.Errorf("AvailBytes = %d, want > 0", m.AvailBytes)
	}
	if m.AvailBytes > m.TotalBytes {
		t.Errorf("AvailBytes (%d) must be <= TotalBytes (%d)", m.AvailBytes, m.TotalBytes)
	}
}

// TestSampleStorage_Unconfigured verifies an empty-Path mount is reported as
// present but not configured, rather than dropped.
func TestSampleStorage_Unconfigured(t *testing.T) {
	paths := fixturePaths(t, cpuStatFixture, memCurrentFixture, memMaxFixture, netDevFixture, ioStatFixture, diskstatsFixture)
	s, err := sampleFromPaths(paths, []MountSpec{{Name: "foo", Path: ""}})
	if err != nil {
		t.Fatalf("sampleFromPaths: %v", err)
	}
	if len(s.StorageMounts) != 1 {
		t.Fatalf("StorageMounts len = %d, want 1", len(s.StorageMounts))
	}
	m := s.StorageMounts[0]
	if m.Configured {
		t.Errorf("StorageMounts[0].Configured = true, want false (empty path)")
	}
	if m.Name != "foo" {
		t.Errorf("StorageMounts[0].Name = %q, want %q", m.Name, "foo")
	}
}

func TestComputeRates(t *testing.T) {
	t0 := time.Now()
	numCPU := runtime.NumCPU()
	prev := RawSample{
		CapturedAt:          t0,
		CPUUsageMicros:      0,
		MemUsedBytes:        100,
		MemLimitBytes:       1000,
		NetRxBytes:          0,
		NetTxBytes:          0,
		ContainerDiskRBytes: 0,
		ContainerDiskWBytes: 0,
		ServerDisks:         []DiskRaw{{Name: "sda", RBytes: 0, WBytes: 0}},
	}
	// 2 seconds later. CPU consumed a full core-second (1_000_000 usec) over
	// 2s across numCPU cores → (1e6/2/1e6/numCPU)*100 = 50/numCPU percent.
	curr := RawSample{
		CapturedAt:          t0.Add(2 * time.Second),
		CPUUsageMicros:      1_000_000,
		MemUsedBytes:        200,
		MemLimitBytes:       1000,
		NetRxBytes:          4000, // 2000 B/s
		NetTxBytes:          8000, // 4000 B/s
		ContainerDiskRBytes: 2000, // 1000 B/s
		ContainerDiskWBytes: 6000, // 3000 B/s
		ServerDisks:         []DiskRaw{{Name: "sda", RBytes: 1024, WBytes: 2048}},
		StorageMounts: []StorageEntry{
			{Name: "App data", TotalBytes: 1 << 40, AvailBytes: 1 << 39, Configured: true},
			{Name: "Movies", Configured: false},
		},
	}
	snap := ComputeRates(prev, curr)

	wantCPU := 50.0 / float64(numCPU)
	if diff := snap.CPUPercent - wantCPU; diff > 0.001 || diff < -0.001 {
		t.Errorf("CPUPercent = %f, want %f", snap.CPUPercent, wantCPU)
	}
	if snap.MemUsedBytes != 200 {
		t.Errorf("MemUsedBytes = %d, want 200", snap.MemUsedBytes)
	}
	if snap.NetRxBPS != 2000 {
		t.Errorf("NetRxBPS = %f, want 2000", snap.NetRxBPS)
	}
	if snap.NetTxBPS != 4000 {
		t.Errorf("NetTxBPS = %f, want 4000", snap.NetTxBPS)
	}
	if snap.ContainerDiskReadBPS != 1000 {
		t.Errorf("ContainerDiskReadBPS = %f, want 1000", snap.ContainerDiskReadBPS)
	}
	if snap.ContainerDiskWriteBPS != 3000 {
		t.Errorf("ContainerDiskWriteBPS = %f, want 3000", snap.ContainerDiskWriteBPS)
	}
	if len(snap.ServerDisks) != 1 {
		t.Fatalf("ServerDisks len = %d, want 1", len(snap.ServerDisks))
	}
	if snap.ServerDisks[0].ReadBPS != 512 { // 1024 B / 2s
		t.Errorf("sda ReadBPS = %f, want 512", snap.ServerDisks[0].ReadBPS)
	}
	if snap.ServerDisks[0].WriteBPS != 1024 { // 2048 B / 2s
		t.Errorf("sda WriteBPS = %f, want 1024", snap.ServerDisks[0].WriteBPS)
	}
	// Storage mounts are point-in-time, passed through from curr unchanged (not
	// a rate). Length and per-entry fields must match the input.
	if len(snap.StorageMounts) != len(curr.StorageMounts) {
		t.Fatalf("StorageMounts len = %d, want %d", len(snap.StorageMounts), len(curr.StorageMounts))
	}
	for i, e := range curr.StorageMounts {
		got := snap.StorageMounts[i]
		if got.Name != e.Name || got.TotalBytes != e.TotalBytes || got.AvailBytes != e.AvailBytes || got.Configured != e.Configured {
			t.Errorf("StorageMounts[%d] = %+v, want %+v (passthrough from curr)", i, got, e)
		}
	}
}

// TestComputeRates_ZeroElapsed guards the divide-by-zero fallback.
func TestComputeRates_ZeroElapsed(t *testing.T) {
	t0 := time.Now()
	prev := RawSample{CapturedAt: t0, CPUUsageMicros: 0, NetRxBytes: 0}
	curr := RawSample{CapturedAt: t0, CPUUsageMicros: 0, NetRxBytes: 500} // same timestamp
	snap := ComputeRates(prev, curr)
	// elapsed clamped to 1s → 500 B/s, no panic.
	if snap.NetRxBPS != 500 {
		t.Errorf("NetRxBPS = %f, want 500 (1s fallback)", snap.NetRxBPS)
	}
}
