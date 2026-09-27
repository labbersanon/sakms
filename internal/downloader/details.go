package downloader

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	torrentlib "github.com/anacrolix/torrent"
)

// Claude 2026-09-26: Downloads popup torrent telemetry + actions.
// Reason: cards stay compact; Reannounce/Recheck/file priority live here.
// Troubleshooting: popup swarm empty while the torrent handle is live.
// Review if: a dedicated details endpoint replaces list enrichment.

func fillTorrentDetails(d *Download, e *entry) {
	t := e.t
	if t == nil {
		return
	}
	alive := true
	select {
	case <-t.Closed():
		alive = false
	default:
	}
	if !alive {
		return
	}

	stats := t.Stats()
	d.PeerCount = int64(stats.ActivePeers)
	uploaded := stats.BytesWrittenData.Int64()
	if e.seedBaselineUp > 0 && uploaded >= e.seedBaselineUp {
		uploaded = uploaded - e.seedBaselineUp
	}
	if uploaded < 0 {
		uploaded = 0
	}
	d.Uploaded = uploaded
	if d.TotalLength > 0 {
		d.Ratio = float64(uploaded) / float64(d.TotalLength)
	}

	hash := t.InfoHash().HexString()
	d.InfoHash = hash
	name := strings.TrimSpace(t.Name())
	magnet := "magnet:?xt=urn:btih:" + hash
	if name != "" {
		magnet += "&dn=" + url.QueryEscape(name)
	}
	d.Magnet = magnet

	if t.Info() != nil {
		d.PiecesHave, d.PiecesTotal, d.PieceHeatmap, d.Availability = pieceSummary(t)
		d.TorrentFiles = torrentFiles(t)
		d.Trackers = trackerList(t, stats.ActivePeers)
	}
}

func pieceSummary(t *torrentlib.Torrent) (have, total int, heat string, avail float64) {
	total = t.NumPieces()
	if total <= 0 {
		return 0, 0, "", 0
	}
	runs := t.PieceStateRuns()
	complete := 0
	partial := 0
	for _, run := range runs {
		if run.Complete {
			complete += run.Length
		} else if run.Partial {
			partial += run.Length
		}
	}
	have = complete
	avail = float64(complete+partial) / float64(total)
	heat = compressHeatmap(runs, total)
	return have, total, heat, avail
}

func compressHeatmap(runs torrentlib.PieceStateRuns, total int) string {
	const buckets = 64
	n := buckets
	if total < n {
		n = total
	}
	if n <= 0 {
		return ""
	}
	// Expand runs into a 0/1/2 slice (missing / partial / complete), then bucket.
	states := make([]byte, total)
	i := 0
	for _, run := range runs {
		v := byte(0)
		if run.Complete {
			v = 2
		} else if run.Partial {
			v = 1
		}
		for k := 0; k < run.Length && i < total; k++ {
			states[i] = v
			i++
		}
	}
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		start := i * total / n
		end := (i + 1) * total / n
		if end <= start {
			end = start + 1
		}
		var sum int
		for j := start; j < end && j < total; j++ {
			sum += int(states[j])
		}
		span := end - start
		if span < 1 {
			span = 1
		}
		// 0-9 from average of 0..2.
		digit := (sum * 9) / (span * 2)
		if digit > 9 {
			digit = 9
		}
		b.WriteByte(byte('0' + digit))
	}
	return b.String()
}

func torrentFiles(t *torrentlib.Torrent) []TorrentFile {
	src := t.Files()
	out := make([]TorrentFile, 0, len(src))
	for _, f := range src {
		if f == nil {
			continue
		}
		prio := "normal"
		switch f.Priority() {
		case torrentlib.PiecePriorityNone:
			prio = "skip"
		case torrentlib.PiecePriorityHigh:
			prio = "high"
		}
		path := f.DisplayPath()
		if path == "" {
			path = f.Path()
		}
		out = append(out, TorrentFile{
			Path:      path,
			Length:    f.Length(),
			Completed: f.BytesCompleted(),
			Priority:  prio,
		})
	}
	return out
}

func trackerList(t *torrentlib.Torrent, activePeers int) []TrackerStatus {
	mi := t.Metainfo()
	seen := map[string]bool{}
	var urls []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		urls = append(urls, u)
	}
	add(mi.Announce)
	for _, tier := range mi.AnnounceList {
		for _, u := range tier {
			add(u)
		}
	}
	status := "configured"
	if activePeers > 0 {
		status = "working"
	}
	out := make([]TrackerStatus, 0, len(urls))
	for _, u := range urls {
		out = append(out, TrackerStatus{URL: u, Status: status})
	}
	return out
}

// Reannounce nudges DHT and re-adds the current announce list.
func (m *Manager) Reannounce(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	var t *torrentlib.Torrent
	if ok {
		t = e.t
	}
	tc := m.tc
	m.mu.Unlock()
	if !ok || t == nil {
		return fmt.Errorf("download not found: %s", gid)
	}
	if tc != nil {
		for _, s := range tc.DhtServers() {
			done, stop, err := t.AnnounceToDht(s)
			if err != nil || stop == nil {
				continue
			}
			go func() {
				defer stop()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
			}()
		}
	}
	mi := t.Metainfo()
	if len(mi.AnnounceList) > 0 {
		t.AddTrackers(mi.AnnounceList)
	} else if mi.Announce != "" {
		t.AddTrackers([][]string{{mi.Announce}})
	}
	return nil
}

// Recheck starts a background VerifyData of the torrent pieces.
func (m *Manager) Recheck(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	var t *torrentlib.Torrent
	if ok {
		t = e.t
	}
	m.mu.Unlock()
	if !ok || t == nil {
		return fmt.Errorf("download not found: %s", gid)
	}
	go func() {
		if err := t.VerifyData(); err != nil {
			log.Printf("downloader: recheck %s: %v", gid, err)
		}
	}()
	return nil
}

// SetFilePriority sets skip/normal/high on one inner torrent file.
func (m *Manager) SetFilePriority(gid, path, priority string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("file path is required")
	}
	var prio torrentlib.PiecePriority
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "skip":
		prio = torrentlib.PiecePriorityNone
	case "high":
		prio = torrentlib.PiecePriorityHigh
	case "normal", "":
		prio = torrentlib.PiecePriorityNormal
	default:
		return fmt.Errorf("priority must be skip, normal, or high")
	}
	m.mu.Lock()
	e, ok := m.entries[gid]
	var t *torrentlib.Torrent
	if ok {
		t = e.t
	}
	m.mu.Unlock()
	if !ok || t == nil {
		return fmt.Errorf("download not found: %s", gid)
	}
	if t.Info() == nil {
		return fmt.Errorf("torrent metadata is not ready")
	}
	for _, f := range t.Files() {
		if f == nil {
			continue
		}
		if f.DisplayPath() == path || f.Path() == path {
			f.SetPriority(prio)
			return nil
		}
	}
	return fmt.Errorf("file not found: %s", path)
}
