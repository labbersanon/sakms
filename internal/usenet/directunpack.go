package usenet

import (
	"bufio"
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Claude 2026-10-23: On Demand Play Direct Unpack (SABnzbd-style unrar -vp).
// Reason: split RAR is the common Usenet case; waiting for BODY+unpack is 5–30
//   min of Play that is not Play. unrar -vp pauses before each volume; we write
//   Continue once that volume is fully assembled. Volumes stay on disk until
//   finalizeAssembled deletes them (never from this path).
// Troubleshooting: Play stuck on active with no playSrc → part002 never assembled
//   or unrar missing (image has it; wade-pc PATH does not).
// Review if: passworded RARs need the job password list on this path.

type directUnpacker struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	done      chan struct{}
	complete  map[string]bool
	dir       string
	leader    string
	err       error
	extracted bool
}

func tickDirectUnpack(ctx context.Context, dir string, du *directUnpacker, assembled []string) *directUnpacker {
	if dir == "" || len(assembled) == 0 {
		return du
	}
	if du != nil {
		du.setComplete(assembled)
		return du
	}
	names := make([]string, len(assembled))
	copy(names, assembled)
	leaders := archiveLeaders(names)
	for _, leader := range leaders {
		volumes := rarSetScore(leader, names) >= 2 || rarBarePlusR00(names)
		single := isSingleRar(leader, names)
		if !volumes && !single {
			continue
		}
		started := startDirectUnpack(ctx, dir, leader, volumes, names)
		if started != nil {
			return started
		}
	}
	return nil
}

func isSingleRar(leader string, names []string) bool {
	lower := strings.ToLower(leader)
	if archiveKind(leader) != "rar" || rarPartRE.MatchString(lower) || rarVolumeRE.MatchString(lower) {
		return false
	}
	key := archiveSetKey(leader)
	for _, n := range names {
		if n == leader {
			continue
		}
		if archiveSetKey(n) == key && archiveKind(n) == "rar" {
			return false
		}
	}
	return true
}

func startDirectUnpack(ctx context.Context, dir, leader string, volumes bool, assembled []string) *directUnpacker {
	bin, err := lookPath("unrar")
	if err != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	args := []string{"e", "-idp", "-o+", "-p-"}
	if volumes {
		args = append(args, "-vp")
	} else {
		args = append(args, "-y")
	}
	args = append(args, filepath.Join(dir, leader), dir+string(filepath.Separator))
	cmd := unpackCommand(runCtx, bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		log.Printf("usenet: direct unpack stdin %s: %v", leader, err)
		return nil
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		cancel()
		stdin.Close()
		pw.Close()
		pr.Close()
		log.Printf("usenet: direct unpack start %s: %v", leader, err)
		return nil
	}
	du := &directUnpacker{
		cancel:   cancel,
		stdin:    stdin,
		done:     make(chan struct{}),
		complete: map[string]bool{},
		dir:      dir,
		leader:   leader,
	}
	du.setComplete(assembled)
	go du.readPrompts(runCtx, pr)
	go du.watchExtracted(runCtx)
	go func() {
		waitErr := cmd.Wait()
		pw.Close()
		du.mu.Lock()
		du.err = waitErr
		if waitErr == nil {
			du.extracted = du.extracted || len(videoNamesInDir(dir)) > 0
		}
		du.mu.Unlock()
		close(du.done)
	}()
	log.Printf("usenet: direct unpack %s volumes=%v", leader, volumes)
	return du
}

func (du *directUnpacker) setComplete(assembled []string) {
	if du == nil {
		return
	}
	du.mu.Lock()
	defer du.mu.Unlock()
	if du.complete == nil {
		du.complete = map[string]bool{}
	}
	for _, n := range assembled {
		base := filepath.Base(n)
		if base != "" && base != "." {
			du.complete[base] = true
		}
	}
}

func (du *directUnpacker) volumeReady(name string) bool {
	if du == nil || name == "" {
		return false
	}
	du.mu.Lock()
	defer du.mu.Unlock()
	return du.complete[filepath.Base(name)]
}

func (du *directUnpacker) Extracted() bool {
	if du == nil {
		return false
	}
	du.mu.Lock()
	defer du.mu.Unlock()
	return du.extracted
}

func (du *directUnpacker) Wait(ctx context.Context) {
	if du == nil {
		return
	}
	select {
	case <-du.done:
	case <-ctx.Done():
	}
}

func (du *directUnpacker) Stop() {
	if du == nil {
		return
	}
	if du.cancel != nil {
		du.cancel()
	}
	if du.stdin != nil {
		_ = du.stdin.Close()
	}
	select {
	case <-du.done:
	case <-time.After(3 * time.Second):
	}
}

func (du *directUnpacker) watchExtracted(ctx context.Context) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if len(videoNamesInDir(du.dir)) > 0 {
			du.mu.Lock()
			du.extracted = true
			du.mu.Unlock()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-du.done:
			return
		case <-tick.C:
		}
	}
}

func (du *directUnpacker) readPrompts(ctx context.Context, r io.Reader) {
	br := bufio.NewReader(r)
	var acc strings.Builder
	buf := make([]byte, 256)
	waiting := du.leader
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := br.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			s := acc.String()
			if looksVolumePrompt(s) {
				next := volumeFromPrompt(s, waiting)
				if next != "" && du.waitVolume(ctx, next) {
					if _, werr := io.WriteString(du.stdin, "C\n"); werr != nil {
						return
					}
					waiting = next
				}
				acc.Reset()
			}
			if acc.Len() > 8192 {
				keep := s[len(s)-2048:]
				acc.Reset()
				acc.WriteString(keep)
			}
		}
		if err != nil {
			return
		}
	}
}

func (du *directUnpacker) waitVolume(ctx context.Context, name string) bool {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if du.volumeReady(name) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-du.done:
			return du.volumeReady(name)
		case <-tick.C:
		}
	}
}

func looksVolumePrompt(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "[c]") ||
		strings.Contains(lower, "insert disk") ||
		strings.Contains(lower, "cannot find volume") ||
		strings.Contains(lower, "next volume") ||
		strings.Contains(lower, "continue extraction")
}

func volumeFromPrompt(s, leader string) string {
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, ".,;:[]")
		base := filepath.Base(f)
		if archiveKind(base) == "rar" {
			return base
		}
	}
	return nextRarVolume(leader)
}
