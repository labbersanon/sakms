package usenet

import (
	"errors"
	"io"
	"log"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/Tensai75/nntp"
)

// articleIOTimeout bounds one NNTP BODY round-trip, including the article
// payload. NZBGet's ArticleTimeout default is 60s. STAT uses the article
// ctx deadline when it is sooner (statArticleTimeout). 0 disables the
// deadline (tests only).
//
// Claude 2026-10-10: per-socket read/write deadline around BODY/STAT.
// Reason: Tensai75/nntp never sets a deadline on the underlying net.Conn. A
//
//	server that accepts BODY then stalls (TCP keepalives still ACKing) blocks
//	bufio.Reader.ReadBytes forever. downloadAll never returns, dl.err stays
//	nil, sweepUsenetFailures skips the grab, and usenet_max_concurrent_downloads=1
//	stays occupied.
//
// Troubleshooting: Downloads "attempting" for days at 0 B/s; last journal line
//
//	is "resuming — N segment(s) already complete"; queued NZBs deferred
//	"usenet download slots full".
//
// Review if: Tensai75/nntp exports SetDeadline, or this becomes a settings knob.
var articleIOTimeout = 60 * time.Second

var (
	nntpDeadlineWarn  sync.Once
	errNoNNTPDeadline = errors.New("usenet: NNTP connection does not expose a deadline")
)

type deadlineSetter interface {
	SetDeadline(time.Time) error
}

func nntpUnderlying(c *nntp.Conn) (any, error) {
	if c == nil {
		return nil, errNoNNTPDeadline
	}
	f := reflect.ValueOf(c).Elem().FieldByName("conn")
	if !f.IsValid() || f.IsNil() {
		return nil, errNoNNTPDeadline
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface(), nil
}

// setNNTPDeadline applies t to the unexported net.Conn inside c. Zero t
// clears the deadline so a pooled socket is not carrying a stale timeout.
func setNNTPDeadline(c *nntp.Conn, t time.Time) error {
	wc, err := nntpUnderlying(c)
	if err != nil {
		return err
	}
	d, ok := wc.(deadlineSetter)
	if !ok {
		return errNoNNTPDeadline
	}
	return d.SetDeadline(t)
}

// discardNNTP closes the underlying TCP/TLS socket without waiting for QUIT.
// Quit hangs when the peer is stuck in a BODY/STAT that never replies — Close
// unblocks both our reader and the server's.
func discardNNTP(c *nntp.Conn) {
	if c == nil {
		return
	}
	wc, err := nntpUnderlying(c)
	if err != nil {
		_ = setNNTPDeadline(c, time.Now())
		_ = c.Quit()
		return
	}
	if closer, ok := wc.(io.Closer); ok {
		_ = closer.Close()
		return
	}
	_ = setNNTPDeadline(c, time.Now())
	_ = c.Quit()
}

// armNNTPDeadline sets a now+articleIOTimeout deadline and returns a disarm
// func that clears it. Safe to call the disarm more than once.
func armNNTPDeadline(c *nntp.Conn) func() {
	return armNNTPDeadlineUntil(c, time.Now().Add(articleIOTimeout))
}

func armNNTPDeadlineUntil(c *nntp.Conn, until time.Time) func() {
	if articleIOTimeout <= 0 || c == nil || until.IsZero() {
		return func() {}
	}
	if err := setNNTPDeadline(c, until); err != nil {
		nntpDeadlineWarn.Do(func() {
			log.Printf("usenet: NNTP socket deadline unavailable: %v — hung BODY reads will not time out", err)
		})
		return func() {}
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = setNNTPDeadline(c, time.Time{}) })
	}
}
