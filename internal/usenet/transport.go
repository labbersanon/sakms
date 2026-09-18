package usenet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/Tensai75/nntp"
)

// ErrTransport marks a retrievable failure of the CONNECTION rather than of
// the article: a dropped socket, a reset, a dial failure, a truncated body.
var ErrTransport = errors.New("usenet: transport failure")

const (
	// maxSegmentAttemptsPerServer is how many times fetchSegmentAny tries one
	// pool before moving to the next. 3 = original attempt + 2 reconnects.
	maxSegmentAttemptsPerServer = 3
	// maxStatAttemptsPerServer is the same bound for statArticleAny (STAT is
	// cheaper than BODY, so 2 is sufficient).
	maxStatAttemptsPerServer = 2
)

// transportRetryDelay returns the sleep before the given retry attempt number
// (1-indexed). The delay is cancellable via ctx.Done().
// attempt 1 → 250 ms, attempt 2+ → 750 ms.
func transportRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 250 * time.Millisecond
	}
	return 750 * time.Millisecond
}

// isTransportError reports whether err is a connection-level failure that
// warrants a single reconnect attempt (pool.put(conn, false) already discards
// the socket and releases the live token, so a fresh dial is possible).
//
// The order below is the precedence: everything that proves the socket is still
// alive is ruled out first, then typed network errors, then a substring fallback
// for the string-only errors Tensai75/nntp and rapidyenc produce.
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Protocol-level answers: socket is alive.
	if errors.Is(err, ErrArticleNotFound) || errors.Is(err, ErrArticleRemoved) {
		return false
	}
	// Shutdown signals: a cancel is a restart signal, not a retrieval failure.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// An nntp.Error that survived mapNNTPError is a protocol response —
	// conservative: keep today's behaviour rather than treating it as transport.
	var nntpErr nntp.Error
	if errors.As(err, &nntpErr) {
		return false
	}
	// Typed I/O sentinels.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// POSIX socket errors.
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	// *net.OpError covers "write tcp IP:PORT->IP:PORT: write: broken pipe" —
	// the shape of the 14 live segment failures observed in the plan evidence.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// net.Error with Timeout() covers per-connection read/write deadlines.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, sub := range []string{
		"broken pipe",
		"connection reset by peer",
		"connection closed",
		"use of closed network connection",
		"i/o timeout",
		"unexpected eof",
		"no route to host",
		"tls: ",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// classifySegmentFailure wraps err with ErrTransport when it is a connection-
// level failure. Called exactly once on fetchSegmentAny's final otherErr return,
// so the marker travels up through assembleFile → downloadAll → dl.err →
// Download.Err → onError / sweepUsenetFailures. errors.Is(failure, ErrTransport)
// is the api-side test.
func classifySegmentFailure(err error) error {
	if isTransportError(err) {
		// Claude 2026-09-17: multi-%w wraps both the sentinel and the cause so
		// errors.Is(out, ErrTransport) and errors.Is(out, cause) both hold.
		// Reason: the api layer tests errors.Is(…, ErrTransport); the log line
		//   needs the original error text for diagnosis.
		// Review if: a Go version < 1.20 is ever required (multi-%w is 1.20+).
		return fmt.Errorf("%w: %w", ErrTransport, err)
	}
	return err
}
