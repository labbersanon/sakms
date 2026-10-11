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
// with ErrNoVideoUnpacked unless some peek has video or archive magic.
//
// Claude 2026-09-28: first-segment video gate after STAT.
// Reason: STAT only proves articles exist; nfo/mp3/jpeg NZBs still downloaded.
//
//	First-segment magic rejects nfo/jpeg NZBs without assembling. Obfuscated
//	.par2 that is Matroska still passes (same sniff as verifyAndRepair). Resume
//	skip map skips the gate so a partial staging dir is not rejected.
//
// Troubleshooting: journal "usenet precheck: abort — no usable video".
// Review if: RAR first-volume `unrar l` is added when FileDesc names are hashes.
//
// Claude 2026-10-10: require video/archive magic; FileDesc/filename is not proof.
// Reason: PAR2 FileDesc listing movie.mkv, a claimed .mkv name, or EBML bytes
//
//	inside a compressed first segment all logged "video peek ok" then unpack
//	produced no video (Love Is Blind hash-named NZBs 2026-10-10; ELiTE PAR2).
//
// Troubleshooting: journal "usenet precheck: abort — no usable video" after
//
//	only PAR2/unknown peeks; "video peek ok" must name a file whose payload
//	magic is Matroska/MP4/RAR/ZIP.
//
// Review if: encrypted first-segments with no magic become common (those
//
//	already fail unpack today).
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

	peeked := 0
	for i, t := range targets {
		if peekCtx.Err() != nil {
			return videoPeekTimeoutResult(peeked, i, len(targets), peekCtx.Err())
		}
		if progressGID != "" {
			m.setPhaseProgress(progressGID, int64(i+1), int64(len(targets)))
		}
		seg, err := m.fetchSegmentAny(peekCtx, t.msgID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return videoPeekTimeoutResult(peeked, i, len(targets), err)
			}
			continue
		}
		peeked++
		ev := classifyPeek(seg.data)
		switch ev {
		case evidenceVideo, evidenceArchive:
			if ev == evidenceArchive && rarLooksEncrypted(seg.data) && !m.hasUnpackPassword(progressGID) {
				// Claude 2026-10-11: Discover/AI when NZB+sidecar have no password.
				// Reason: sibling indexer titles often carry {{pw}}; aborting peek
				//   without a search parked Smad RARs that a Discover hit would unlock.
				// Troubleshooting: journal "discovered N unpack password candidate(s)".
				// Review if: a per-job UI password field replaces search.
				m.discoverJobPasswords(ctx, progressGID)
				if !m.hasUnpackPassword(progressGID) {
					log.Printf("usenet precheck: abort — encrypted RAR and no password file=%q", peekLabel(t.subject, seg.filename))
					return ErrPasswordProtected
				}
			}
			log.Printf("usenet precheck: video peek ok file=%q", peekLabel(t.subject, seg.filename))
			return nil
		}
	}

	if peeked == 0 {
		return nil
	}
	log.Printf("usenet precheck: abort — no usable video in %d first-segment peek(s)", peeked)
	return ErrNoVideoUnpacked
}

func videoPeekTimeoutResult(peeked, done, total int, err error) error {
	if peeked == 0 {
		log.Printf("usenet precheck: video peek inconclusive (%v after %d/%d files)", err, done, total)
		return nil
	}
	log.Printf("usenet precheck: abort — no video/archive magic in %d peek(s) before timeout", peeked)
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

func classifyPeek(data []byte) videoEvidence {
	kind := sniffPayloadKind(data)
	// Prefix video magic only. videoMagicIn (magic anywhere in the segment)
	// false-positives on compressed PAR2/RAR and was treating hash-named junk
	// as a video. Archive peeks still search inside the container below.
	if kind == kindVideo {
		return evidenceVideo
	}
	if kind == kindArchive {
		return classifyArchivePeek(data)
	}
	if kind == kindPAR2 {
		return classifyPAR2Peek(data)
	}
	if kind == kindJunk {
		return evidenceJunk
	}
	// A claimed .mkv/.rar name without matching magic is not proof — the
	// body may be nfo, encrypted, or an empty hash. Keep scanning.
	return evidenceUnknown
}

func classifyArchivePeek(data []byte) videoEvidence {
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
	// Member names and a claimed video filename are hints; the RAR/ZIP
	// prefix itself is enough to proceed (unrar will confirm).
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
		if config.IsVideoFile(name) || archiveKind(name) != "" {
			// FileDesc is a claim, not payload magic. Keep peeking other
			// NZB files for a real Matroska/RAR prefix.
			allJunk = false
			continue
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
