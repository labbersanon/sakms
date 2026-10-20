package usenetsearch

import (
	"regexp"
	"strings"
)

// Claude 2026-10-20: ngPost posting layout (github.com/mbruel/ngPost).
// Reason: live hash.7z is compress+gen_name+gen_pass+7z, not -x UUID
//   subjects. Archive name is randomPass [A-Za-z0-9], not md5(title).
//   Password lives only in the NZB. 7z is -mx0 -mhe=on -p. PAR2 covers
//   the volumes. GROUP_POLICY ALL duplicates the same msgid per group.
// Troubleshooting: ObfKey still splits one post across groups → From is
//   not @ngPost.com (fixed FROM). skip_7z=false after attempts → msgid
//   host is not ngPost. Review if: a poster uses a fixed FROM or msg_id
//   other than ngPost.
// Related files: subject.go ObfKey, namefix.go pickBodyTargets

var (
	reNgPostFrom  = regexp.MustCompile(`(?i)^[A-Za-z0-9]{8,20}@ngPost\.com$`)
	reNgPostMsgID = regexp.MustCompile(`(?i)@ngPost(?:\.com)?>?$`)
)

func looksLikeNgPostFrom(from string) bool {
	return reNgPostFrom.MatchString(strings.TrimSpace(from))
}

func looksLikeNgPostMsgID(msgid string) bool {
	return reNgPostMsgID.MatchString(strings.TrimSpace(msgid))
}

func looksLikeNgPost(rows []HeaderRow) bool {
	for _, r := range rows {
		if looksLikeNgPostFrom(r.From) || looksLikeNgPostMsgID(r.MsgID) {
			return true
		}
	}
	return false
}
