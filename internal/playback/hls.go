package playback

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/mediainfo"
)

const (
	defaultIdle = 5 * time.Minute
	defaultWait = 90 * time.Second
	followProbe = "10000000"
)

var segmentName = regexp.MustCompile(`^(index\.m3u8|init\.mp4|seg\d+\.m4s)$`)

// Claude 2026-10-14: Jellyfin-matching HLS for in-app Play.
// Reason: Chrome cannot Direct Play Matroska; a copy-to-pipe fMP4 cannot seek
//   and fails on AC3/HEVC. Probe at session start → remux / AAC / H.264.
// Troubleshooting: invalid {file} 400; ffmpeg missing 500; playlist wait 90s.
// Review if: native TV HLS or per-seek ffmpeg restart lands.

// Hub owns ffmpeg HLS sessions keyed by path+mtime+size.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*session

	probe       func(ctx context.Context, path string) (videoCodec, audioCodec string, err error)
	lookPath    func(string) (string, error)
	runCmd      func(ctx context.Context, name string, args ...string) *exec.Cmd
	detectNVENC func() bool
	// startFake, when set, writes the session dir instead of forking ffmpeg.
	startFake func(ctx context.Context, src, outDir string, d Decision) error

	idle    time.Duration
	wait    time.Duration
	tmpRoot string

	nvencOnce sync.Once
	nvenc     bool
}

type session struct {
	hub    *Hub
	key    string
	dir    string
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	err   error
	last  time.Time
	timer *time.Timer
}

var defaultHub = NewHub()

// NewHub is the production HLS manager (real ffprobe/ffmpeg).
func NewHub() *Hub {
	return &Hub{
		sessions: map[string]*session{},
		probe:    defaultProbe,
		lookPath: exec.LookPath,
		runCmd:   exec.CommandContext,
		idle:     defaultIdle,
		wait:     defaultWait,
	}
}

// NewFakeHub is a Hub that never forks ffmpeg. Tests in other packages use it
// with ReplaceHub so GET .../hls/{file} stays off PATH.
func NewFakeHub(
	probe func(ctx context.Context, path string) (videoCodec, audioCodec string, err error),
	start func(ctx context.Context, src, outDir string, d Decision) error,
) *Hub {
	h := NewHub()
	h.probe = probe
	h.startFake = start
	h.idle = time.Hour
	return h
}

// ReplaceHub swaps the package hub used by ServeHLS. Tests only.
func ReplaceHub(h *Hub) func() {
	prev := defaultHub
	defaultHub = h
	return func() { defaultHub = prev }
}

// ServeHLS serves one allowlisted HLS artifact for src using the package hub.
func ServeHLS(w http.ResponseWriter, r *http.Request, src, file string) {
	defaultHub.Serve(w, r, src, file)
}

// ServeHLSFollow is On Demand Play: one ffmpeg session per path while the file
// grows. Library ServeHLS stays size-keyed so a replaced library file remuxes.
func ServeHLSFollow(w http.ResponseWriter, r *http.Request, src, file string, growing func() bool) {
	defaultHub.ServeFollow(w, r, src, file, growing)
}

// AllowedSegment is the {file} allowlist: index.m3u8, init.mp4, segN.m4s.
func AllowedSegment(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return false
	}
	return segmentName.MatchString(name)
}

func defaultProbe(ctx context.Context, path string) (string, string, error) {
	p, err := mediainfo.New().Probe(ctx, path)
	if err != nil {
		return "", "", err
	}
	return p.CodecName, p.AudioCodec, nil
}

// Serve writes one HLS file for src. ffmpeg is started on first request.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, src, file string) {
	h.serve(w, r, src, file, false, nil)
}

// ServeFollow writes one HLS file for a growing src. Sessions are keyed by
// path only so a size change does not restart ffmpeg.
func (h *Hub) ServeFollow(w http.ResponseWriter, r *http.Request, src, file string, growing func() bool) {
	h.serve(w, r, src, file, true, growing)
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request, src, file string, follow bool, growing func() bool) {
	if !AllowedSegment(file) {
		http.Error(w, "invalid hls file", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "video file not found", http.StatusNotFound)
			return
		}
		http.Error(w, "video file is not accessible", http.StatusInternalServerError)
		return
	}
	if st.IsDir() {
		http.Error(w, "video path is a directory", http.StatusNotFound)
		return
	}
	sess, err := h.getOrStart(src, st, follow, growing)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dest := filepath.Join(sess.dir, file)
	if err := sess.waitFile(r.Context(), dest); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if file == "index.m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "private, no-cache")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	}
	http.ServeFile(w, r, dest)
}

func sessionKey(path string, st os.FileInfo) string {
	return fmt.Sprintf("%s\x00%d\x00%d", path, st.ModTime().UnixNano(), st.Size())
}

func followSessionKey(path string) string {
	return path + "\x00follow"
}

func (h *Hub) getOrStart(src string, st os.FileInfo, follow bool, growing func() bool) (*session, error) {
	key := sessionKey(src, st)
	if follow {
		key = followSessionKey(src)
	}
	h.mu.Lock()
	s := h.sessions[key]
	if s != nil && !s.failed() {
		s.touchLocked()
		h.mu.Unlock()
		return s, nil
	}
	if s != nil {
		delete(h.sessions, key)
	}
	h.mu.Unlock()
	if s != nil {
		go s.reap()
	}

	parent := h.tmpRoot
	if parent == "" {
		parent = os.TempDir()
	}
	dir, err := os.MkdirTemp(parent, "sakms-hls-")
	if err != nil {
		return nil, fmt.Errorf("hls session: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s = &session{
		hub:    h,
		key:    key,
		dir:    dir,
		cancel: cancel,
		done:   make(chan struct{}),
		last:   time.Now(),
	}
	if err := h.spawn(ctx, s, src, follow, growing); err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, err
	}

	h.mu.Lock()
	if existing := h.sessions[key]; existing != nil {
		h.mu.Unlock()
		cancel()
		<-s.done
		os.RemoveAll(dir)
		existing.touch()
		return existing, nil
	}
	s.timer = time.AfterFunc(h.idle, s.expire)
	h.sessions[key] = s
	h.mu.Unlock()
	return s, nil
}

func (h *Hub) spawn(ctx context.Context, s *session, src string, follow bool, growing func() bool) error {
	// Claude 2026-10-23: follow HLS always transcodes; skip probe on a growing file.
	// Reason: Truncate-up mkv fails ffprobe (41-byte 500). HEVC 10-bit LAMA and
	//   incomplete clusters cannot copy into fMP4. Probe-or-500 is library-only.
	// Troubleshooting: GET .../hls/index.m3u8 500 "hls probe: ffprobe failed".
	// Review if: follow remux of complete-enough h264+aac is added.
	d := Transcode
	hasAudio := true
	if !follow {
		v, a, err := h.probe(ctx, src)
		if err != nil {
			return fmt.Errorf("hls probe: %w", err)
		}
		d = Decide(v, a)
		hasAudio = a != ""
	}
	if h.startFake != nil {
		go func() {
			defer close(s.done)
			if err := h.startFake(ctx, src, s.dir, d); err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
			}
		}()
		return nil
	}
	look := h.lookPath
	if look == nil {
		look = exec.LookPath
	}
	bin, err := look("ffmpeg")
	if err != nil {
		return fmt.Errorf("hls: ffmpeg not on PATH: %w", err)
	}
	run := h.runCmd
	if run == nil {
		run = exec.CommandContext
	}
	var grow *GrowReader
	var args []string
	if follow {
		grow, err = NewGrowReader(ctx, src, growing)
		if err != nil {
			return fmt.Errorf("hls follow: %w", err)
		}
		args = ffmpegHLSPipeArgs(src, s.dir, d, h.useNVENC(), hasAudio)
	} else {
		args = ffmpegHLSArgs(src, s.dir, d, h.useNVENC(), hasAudio)
	}
	cmd := run(ctx, bin, args...)
	errFile, err := os.Create(filepath.Join(s.dir, "ffmpeg.err"))
	if err != nil {
		if grow != nil {
			grow.Close()
		}
		return err
	}
	if grow != nil {
		cmd.Stdin = grow
	}
	cmd.Stderr = errFile
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		errFile.Close()
		if grow != nil {
			grow.Close()
		}
		return fmt.Errorf("hls ffmpeg: %w", err)
	}
	go func() {
		defer errFile.Close()
		if grow != nil {
			defer grow.Close()
		}
		waitErr := cmd.Wait()
		s.mu.Lock()
		if waitErr != nil && ctx.Err() == nil {
			msg, _ := os.ReadFile(filepath.Join(s.dir, "ffmpeg.err"))
			trim := strings.TrimSpace(string(msg))
			if trim == "" {
				trim = waitErr.Error()
			}
			if len(trim) > 2048 {
				trim = trim[:2048]
			}
			s.err = fmt.Errorf("hls ffmpeg: %s", trim)
			log.Printf("hls: %s: %v", src, s.err)
		}
		s.mu.Unlock()
		close(s.done)
	}()
	return nil
}

func (h *Hub) useNVENC() bool {
	h.nvencOnce.Do(func() {
		if h.detectNVENC != nil {
			h.nvenc = h.detectNVENC()
			return
		}
		h.nvenc = ffmpegHasNVENC(h.lookPath, h.runCmd)
	})
	return h.nvenc
}

func ffmpegHasNVENC(look func(string) (string, error), run func(context.Context, string, ...string) *exec.Cmd) bool {
	if look == nil {
		look = exec.LookPath
	}
	bin, err := look("ffmpeg")
	if err != nil {
		return false
	}
	if run == nil {
		run = exec.CommandContext
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := run(ctx, bin, "-hide_banner", "-encoders")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "h264_nvenc")
}

func ffmpegHLSArgs(src, outDir string, d Decision, nvenc, hasAudio bool) []string {
	return appendHLSOutput([]string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", src,
	}, outDir, d, nvenc, hasAudio)
}

func ffmpegHLSPipeArgs(src, outDir string, d Decision, nvenc, hasAudio bool) []string {
	head := []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "+genpts+discardcorrupt",
		"-probesize", followProbe, "-analyzeduration", followProbe,
	}
	if f := ffmpegDemuxFormat(src); f != "" {
		head = append(head, "-f", f)
	}
	head = append(head, "-i", "pipe:0")
	return appendHLSOutput(head, outDir, d, nvenc, hasAudio)
}

func ffmpegDemuxFormat(src string) string {
	switch strings.ToLower(filepath.Ext(src)) {
	case ".mkv":
		return "matroska"
	case ".mp4", ".m4v":
		return "mp4"
	case ".webm":
		return "webm"
	case ".mov":
		return "mov"
	case ".avi":
		return "avi"
	case ".ts", ".m2ts":
		return "mpegts"
	default:
		return ""
	}
}

func appendHLSOutput(head []string, outDir string, d Decision, nvenc, hasAudio bool) []string {
	args := append(head, "-map", "0:v:0")
	if hasAudio {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn", "-dn")
	switch d {
	case Remux:
		args = append(args, "-c", "copy")
	case DirectStream:
		args = append(args, "-c:v", "copy")
		if hasAudio {
			args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
		}
	default:
		args = append(args, videoEncodeArgs(nvenc)...)
		if hasAudio {
			args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
		}
		args = append(args, "-force_key_frames", "expr:gte(t,n_forced*6)", "-sc_threshold", "0")
	}
	return append(args,
		"-f", "hls",
		"-hls_time", "6",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(outDir, "seg%d.m4s"),
		filepath.Join(outDir, "index.m3u8"),
	)
}

func videoEncodeArgs(nvenc bool) []string {
	if nvenc {
		return []string{"-c:v", "h264_nvenc", "-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0", "-pix_fmt", "yuv420p"}
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p"}
}

func (s *session) waitFile(ctx context.Context, dest string) error {
	s.touch()
	wait := s.hub.wait
	if wait <= 0 {
		wait = defaultWait
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(dest); err == nil {
			s.touch()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err := s.failErr(); err != nil {
				return err
			}
			return fmt.Errorf("hls: timeout waiting for %s", filepath.Base(dest))
		case <-s.done:
			if _, err := os.Stat(dest); err == nil {
				return nil
			}
			if err := s.failErr(); err != nil {
				return err
			}
			return fmt.Errorf("hls: ffmpeg exited before %s", filepath.Base(dest))
		case <-tick.C:
		}
	}
}

func (s *session) failErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *session) failed() bool {
	select {
	case <-s.done:
		return s.failErr() != nil
	default:
		return false
	}
}

func (s *session) reap() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.cancel()
	<-s.done
	os.RemoveAll(s.dir)
}

func (s *session) touch() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.touchLocked()
}

func (s *session) touchLocked() {
	s.last = time.Now()
	if s.timer != nil {
		s.timer.Reset(s.hub.idle)
	}
}

func (s *session) expire() {
	s.hub.mu.Lock()
	cur := s.hub.sessions[s.key]
	if cur != s {
		s.hub.mu.Unlock()
		return
	}
	delete(s.hub.sessions, s.key)
	s.hub.mu.Unlock()
	s.reap()
}
