package usenet

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Claude 2026-09-26: Downloads popup helpers (segment/file/STAT/disk/conns).
// Reason: cards stay compact; buildDownload needs cheap derived fields.
// Troubleshooting: popup current file / sidecar age empty on an active NZB.
// Review if: a dedicated details endpoint replaces list enrichment.

func nzbSegmentCount(nzb *NZB) int64 {
	if nzb == nil {
		return 0
	}
	var n int64
	for _, f := range nzb.Files {
		n += int64(len(f.Segs))
	}
	return n
}

func (m *Manager) setSegmentTotals(gid string, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	dl.segmentTotal = total
}

func (m *Manager) setCurrentFile(gid, name string, seg, segTotal int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	dl.currentFile = filepath.Base(name)
	dl.currentSeg = seg
	dl.currentSegTotal = segTotal
}

func (m *Manager) addSegmentDone(gid string, n int64) {
	if n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	dl.segmentDone += n
	if dl.currentSeg < dl.currentSegTotal {
		dl.currentSeg++
	}
}

func (m *Manager) setRepairFile(gid, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	dl.repairFile = filepath.Base(name)
}

func (m *Manager) setFailingSegment(gid, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	dl.failingSegment = msg
}

// connectionCountsLocked sums live NNTP sockets vs max across pools.
// Caller must hold m.mu.
func (m *Manager) connectionCountsLocked() (active, max int) {
	for _, p := range m.pools {
		if p == nil {
			continue
		}
		max += cap(p.live)
		active += len(p.live)
	}
	return active, max
}

func sidecarAgeSec(resume *resumeTracker, stagingDir string) *int64 {
	var last time.Time
	if resume != nil {
		resume.mu.Lock()
		last = resume.lastPersist
		resume.mu.Unlock()
	}
	if last.IsZero() && stagingDir != "" {
		fi, err := os.Stat(filepath.Join(stagingDir, ResumeFileName))
		if err != nil {
			return nil
		}
		last = fi.ModTime()
	}
	if last.IsZero() {
		return nil
	}
	sec := int64(time.Since(last).Seconds())
	if sec < 0 {
		sec = 0
	}
	return &sec
}

var failingSegmentRe = regexp.MustCompile(`(?i)segment\s+(\d+)`)

func failingSegmentFromError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	if failingSegmentRe.MatchString(msg) {
		return msg
	}
	return ""
}
