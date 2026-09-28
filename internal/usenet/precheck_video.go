package usenet

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"time"

	par2lib "github.com/go-newsgroups/par2"
	"github.com/labbersanon/sakms/internal/config"
)

type videoEvidence int

const (
	evidenceUnknown videoEvidence = iota
	evidenceVideo
	evidenceArchive
	evidenceJunk
)

const (
	videoPeekTimeoutBase = 15 * time.Second
	videoPeekTimeoutPer  = 2 * time.Second
	videoPeekTimeoutMax  = 3 * time.Minute
)

func videoPeekTimeout(n int) time.Duration {
	if n < 1 {
		return videoPeekTimeoutBase
	}
	d := videoPeekTimeoutBase + time.Duration(n)*videoPeekTimeoutPer
	if d > videoPeekTimeoutMax {
		return videoPeekTimeoutMax
	}
	return d
}

// precheckVideo BODY-peeks the first segment of each payload leader and aborts
// with ErrNoVideoUnpacked when every peek is decorative junk. Inconclusive
// (unknown magic, truncated PAR2, archive with no inner names) proceeds.
//
// Claude 2026-09-28: first-segment video gate after STAT.
// Reason: STAT only proves articles exist; nfo/mp3/jpeg NZBs still downloaded.
//
//	First-segment magic, PAR2 FileDesc, and ZIP/RAR member names are enough to
//	reject those without assembling. Obfuscated .par2 that is Matroska still
//	passes (same sniff as verifyAndRepair). Resume skip map skips the gate so
//	a partial staging dir is not rejected.
//
// Troubleshooting: journal "usenet precheck: abort — no usable video".
// Review if: RAR first-volume `unrar l` is added when FileDesc names are hashes.
func (m *Manager) precheckVideo(ctx context.Context, payload []NZBFile, skip map[string]bool, progressGID string) error {
	if len(skip) > 0 {
		return nil
	}
	targets := videoPeekTargets(payload)
	if len(targets) == 0 {
		return nil
	}

	peekCtx, cancel := context.WithTimeout(ctx, videoPeekTimeout(len(targets)))
	defer cancel()

	var sawArchive, sawUnknown bool
	peeked := 0
	junk := 0
	for i, t := range targets {
		if peekCtx.Err() != nil {
			log.Printf("usenet precheck: video peek inconclusive (timeout after %d/%d files)", i, len(targets))
			return nil
		}
		if progressGID != "" {
			m.setPhaseProgress(progressGID, int64(i+1), int64(len(targets)))
		}
		seg, err := m.fetchSegmentAny(peekCtx, t.msgID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("usenet precheck: video peek inconclusive (%v after %d/%d files)", err, i, len(targets))
				return nil
			}
			sawUnknown = true
			continue
		}
		peeked++
		ev := classifyPeek(seg.data, seg.filename, t.subject, seg.offset)
		switch ev {
		case evidenceVideo:
			log.Printf("usenet precheck: video peek ok file=%q", peekLabel(t.subject, seg.filename))
			return nil
		case evidenceArchive:
			sawArchive = true
		case evidenceJunk:
			junk++
		default:
			sawUnknown = true
		}
	}

	if peeked == 0 || junk != peeked || sawArchive || sawUnknown {
		return nil
	}
	log.Printf("usenet precheck: abort — no usable video in %d first-segment peek(s)", peeked)
	return ErrNoVideoUnpacked
}

type peekTarget struct {
	msgID   string
	subject string
}

func videoPeekTargets(payload []NZBFile) []peekTarget {
	var out []peekTarget
	for _, f := range payload {
		if !shouldPeekFile(f.Subject) {
			continue
		}
		if len(f.Segs) == 0 {
			continue
		}
		id := strings.TrimSpace(f.Segs[0].MsgID)
		if id == "" {
			continue
		}
		out = append(out, peekTarget{msgID: id, subject: f.Subject})
	}
	return out
}

func shouldPeekFile(subject string) bool {
	name := filenameFromSubject(subject)
	if name == "" {
		name = subject
	}
	return !isRarVolumeNotLeader(name)
}

func peekLabel(subject, yenc string) string {
	if n := filenameFromSubject(subject); n != "" {
		return n
	}
	if yenc != "" {
		return yenc
	}
	return subject
}

func classifyPeek(data []byte, yencName, subject string, offset int64) videoEvidence {
	kind := sniffPayloadKind(data)
	if kind == kindVideo || (offset == 0 && videoMagicIn(data)) {
		return evidenceVideo
	}
	claimed := claimedNames(yencName, subject)
	if kind == kindArchive {
		return classifyArchivePeek(data, claimed)
	}
	if kind == kindPAR2 {
		return classifyPAR2Peek(data)
	}
	if kind == kindJunk {
		return evidenceJunk
	}
	for _, n := range claimed {
		if nameLooksVideo(n) {
			return evidenceVideo
		}
		if nameLooksArchive(n) {
			return evidenceArchive
		}
		if nameLooksJunk(n) {
			return evidenceJunk
		}
	}
	return evidenceUnknown
}

func claimedNames(yencName, subject string) []string {
	var out []string
	if yencName != "" {
		out = append(out, yencName)
	}
	if n := filenameFromSubject(subject); n != "" {
		out = append(out, n)
	}
	return out
}

func classifyArchivePeek(data []byte, claimed []string) videoEvidence {
	if videoMagicIn(data) {
		return evidenceVideo
	}
	inner := archiveMemberNames(data)
	allJunk := len(inner) > 0
	for _, n := range inner {
		if nameLooksVideo(n) {
			return evidenceVideo
		}
		if nameLooksArchive(n) || !nameLooksJunk(n) {
			allJunk = false
		}
	}
	if allJunk {
		return evidenceJunk
	}
	for _, n := range claimed {
		if nameLooksVideo(n) {
			return evidenceVideo
		}
	}
	return evidenceArchive
}

func classifyPAR2Peek(data []byte) videoEvidence {
	rs, err := par2lib.Parse(data)
	if err != nil || rs == nil || len(rs.Files) == 0 {
		// Truncated index or obfuscated non-PAR2 that still started with "PAR2".
		return evidenceUnknown
	}
	allJunk := true
	for _, f := range rs.Files {
		name := filepath.Base(f.Name)
		if config.IsVideoFile(name) {
			return evidenceVideo
		}
		if archiveKind(name) != "" {
			return evidenceArchive
		}
		if !nameLooksJunk(name) {
			allJunk = false
		}
	}
	if allJunk {
		return evidenceJunk
	}
	return evidenceUnknown
}
