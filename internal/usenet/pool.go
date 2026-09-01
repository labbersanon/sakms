package usenet

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Tensai75/nntp"
	"github.com/mnightingale/rapidyenc"
)

// ErrArticleNotFound is returned by fetchSegment when the server responds 430.
var ErrArticleNotFound = errors.New("usenet: article not found (430)")

// ErrArticleRemoved is returned by fetchSegment when the server responds 451
// (typically a DMCA takedown).
var ErrArticleRemoved = errors.New("usenet: article removed (451)")

// defaultMaxConnsPerServer is the connection count substituted whenever a
// ServerConfig arrives with MaxConns <= 0.
//
// Claude 2026-08-01: added alongside multi-subscription support.
// Reason: service_connections.max_conns defaults to 0, and the global
// downloader_max_connections setting is torrent-only now, so nothing else
// supplies a default. A server contributing 0 to the Manager's concurrency
// budget makes errgroup.SetLimit(0) block every Go() call forever — a silent
// total hang rather than an error.
// Troubleshooting: usenet downloads that start and never progress.
// Review if: per-subscription max_conns becomes a required field with its own
// non-zero default enforced at the storage layer.
const defaultMaxConnsPerServer = 4

// ServerConfig holds the parameters needed to dial and authenticate one NNTP
// server. Credentials are provided by the caller from the connections store —
// never hardcoded here.
type ServerConfig struct {
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
	// MaxConns is the number of concurrent connections the pool may hold idle
	// and this server's contribution to the Manager's concurrency budget.
	// Values <= 0 are substituted with defaultMaxConnsPerServer.
	MaxConns int
}

// effectiveMaxConns returns cfg.MaxConns with the defaultMaxConnsPerServer
// substitution applied. Always >= 1.
func effectiveMaxConns(cfg ServerConfig) int {
	if cfg.MaxConns <= 0 {
		return defaultMaxConnsPerServer
	}
	return cfg.MaxConns
}

// pool recycles authenticated NNTP connections to a single server and hard-caps
// how many live sockets exist for that server.
//
// Claude 2026-09-01: live-socket hard cap (NZBGet ServerX.Connections pattern).
// Reason: get() used to dial whenever idle was empty, so N concurrent NZBs each
//   running errgroup.SetLimit(MaxConns) could open N×MaxConns sockets against a
//   provider that only allows MaxConns — downloads started, then died with
//   in-memory-only errors and stranded grabs.
// Troubleshooting: usenet downloads that write a bit then stall/error while
//   Eweka auth still works for a single connection.
// Review if: multi-server fill logic needs per-server budgets that differ from
//   the Manager's segment SetLimit.
// Related: NZBGet nzbget.conf ServerX.Connections / ArticleRetries.
type pool struct {
	cfg    ServerConfig
	idle   chan *nntp.Conn
	// live has capacity effectiveMaxConns and one token per live TCP socket
	// (idle or checked out). Taking from idle does not consume a new token;
	// dialing does; closing a socket (failed put / drained idle) releases one.
	live   chan struct{}
	mu     sync.Mutex
	closed bool
}

func newPool(cfg ServerConfig) *pool {
	n := effectiveMaxConns(cfg)
	return &pool{
		cfg:  cfg,
		idle: make(chan *nntp.Conn, n),
		live: make(chan struct{}, n),
	}
}

// get returns an idle authenticated connection or dials a new one, never
// exceeding effectiveMaxConns live sockets for this server.
// Returns an error immediately if the pool has been closed.
func (p *pool) get() (*nntp.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("usenet: pool is closed")
	}
	p.mu.Unlock()

	// Prefer an already-open idle connection — it already holds a live token.
	select {
	case c := <-p.idle:
		return c, nil
	default:
	}

	// No idle conn: wait for either a free live slot (dial) or a returned idle.
	select {
	case p.live <- struct{}{}:
		c, err := p.dial()
		if err != nil {
			<-p.live
			return nil, err
		}
		return c, nil
	case c := <-p.idle:
		return c, nil
	}
}

// put returns c to the idle pool. A failed connection (ok=false) is closed and
// discarded — never put a broken connection back into the pool. A connection
// returned after the pool was closed is terminated rather than parked.
//
// Claude 2026-08-01: the closed check and the mutex held across the idle send
// were added with Manager.SetSubscriptions.
// Reason: SetSubscriptions can retire a pool while a segment fetch still holds
// one of its connections. close() only ever terminates connections sitting in
// the idle channel, so there is no use-after-close of a live connection — but
// without this check the in-flight connection would be parked in an abandoned
// channel nobody drains again, leaking the socket for the process lifetime.
// Holding p.mu across the send closes the same window against close() itself.
// Troubleshooting: NNTP connections left ESTABLISHED after a subscription edit.
// Review if: pool gains a context-aware acquire/release that tracks checked-out
// connections directly.
//
// Claude 2026-09-01: releasing the live token on discard/close paths.
// Reason: every dial consumes a live token; discarding the socket without
//   releasing it permanently shrinks the pool's effective MaxConns to zero
//   after enough failures.
// Troubleshooting: usenet downloads that stop making progress after a burst of
//   article/dial errors even though MaxConns is still configured high.
// Review if: the live-token accounting moves into a dedicated counter.
func (p *pool) put(c *nntp.Conn, ok bool) {
	if !ok {
		c.Quit()
		p.releaseLive()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		c.Quit()
		p.releaseLive()
		return
	}
	select {
	case p.idle <- c:
		p.mu.Unlock()
	default:
		// Pool is full (shouldn't happen if callers respect MaxConns, but be safe).
		p.mu.Unlock()
		c.Quit()
		p.releaseLive()
	}
}

func (p *pool) releaseLive() {
	select {
	case <-p.live:
	default:
		// Defensive: a put without a matching dial should not deadlock callers.
	}
}

// close terminates all idle connections in the pool and marks it closed so
// subsequent get() calls fail fast rather than dialling new connections.
// Idempotent — calling it twice is safe. Connections currently checked out by
// an in-flight fetch are not touched here; put() terminates them on return.
func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	var drained []*nntp.Conn
	for done := false; !done; {
		select {
		case c := <-p.idle:
			drained = append(drained, c)
		default:
			done = true
		}
	}
	p.mu.Unlock()
	// Quit does network I/O — never hold p.mu across it.
	for _, c := range drained {
		c.Quit()
		p.releaseLive()
	}
}

func (p *pool) dial() (*nntp.Conn, error) {
	addr := fmt.Sprintf("%s:%d", p.cfg.Host, p.cfg.Port)
	var (
		c   *nntp.Conn
		err error
	)
	if p.cfg.TLS {
		c, err = nntp.DialTLS("tcp", addr, &tls.Config{ServerName: p.cfg.Host})
	} else {
		c, err = nntp.Dial("tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("usenet: dialing %s: %w", addr, err)
	}
	// ModeReader switches mode-switching servers to reader mode; most servers
	// accept it silently and some require it — error is intentionally ignored.
	_ = c.ModeReader()
	if p.cfg.Username != "" {
		if authErr := c.Authenticate(p.cfg.Username, p.cfg.Password); authErr != nil {
			c.Quit()
			return nil, fmt.Errorf("usenet: authenticating to %s: %w", addr, authErr)
		}
	}
	return c, nil
}

// segmentResult holds the yEnc-decoded output of one NNTP article.
type segmentResult struct {
	data     []byte
	offset   int64  // byte offset for io.WriterAt reassembly (from yEnc header)
	partSize int64  // decoded byte count for this segment
	filename string // from yEnc =ybegin header (reliable on first segment only)
	fileSize int64  // total assembled file size from yEnc header
}

// fetchSegment downloads one NNTP article by message-ID and yEnc-decodes it.
//
// NOTE: nntp.Conn's io.Reader is only valid until the next call to any Conn
// method — dec.Next() must fully buffer the decoded data before this function
// returns the connection to the caller (who then calls pool.put). That
// invariant is satisfied here: dec.Next() reads to completion before we return.
func fetchSegment(c *nntp.Conn, msgID string) (segmentResult, error) {
	// Reject message-IDs containing CRLF or any other control character to
	// prevent NNTP command injection via NZB segment chardata.
	if strings.ContainsAny(msgID, "\r\n") {
		return segmentResult{}, fmt.Errorf("usenet: invalid message-id %q", msgID)
	}
	for i := 0; i < len(msgID); i++ {
		if msgID[i] < 0x20 {
			return segmentResult{}, fmt.Errorf("usenet: invalid message-id %q", msgID)
		}
	}
	body, err := c.Body("<" + msgID + ">")
	if err != nil {
		return segmentResult{}, mapNNTPError(err)
	}
	// WithStatusLineAlreadyRead: nntp.Conn.Body() already consumed the
	// "222 Body follows" status line before returning the io.Reader, so the
	// decoder starts reading at the first line of the yEnc body.
	dec := rapidyenc.NewDecoder(newArticleWireReader(body), rapidyenc.WithStatusLineAlreadyRead())
	resp, err := dec.Next()
	if err != nil {
		return segmentResult{}, fmt.Errorf("usenet: yEnc decode %s: %w", msgID, err)
	}
	return segmentResult{
		data:     resp.Data,
		offset:   resp.Metadata.Offset,
		partSize: resp.Metadata.PartSize,
		filename: resp.Metadata.FileName,
		fileSize: resp.Metadata.FileSize,
	}, nil
}

// articleWireReader restores raw NNTP wire framing on top of nntp.Conn's body
// reader.
//
// Claude 2026-08-01: added while building the multi-pool fetch path.
// Reason: nntp.Conn.Body() returns a bodyReader that canonicalises CRLF to a
// bare LF and consumes the terminating "." line without emitting it. rapidyenc's
// decoder splits on the literal "\r\n" and treats "\r\n.\r\n" as the end of the
// article, so it never sees a line break or a terminator and every single
// decode failed with io.ErrUnexpectedEOF. This adapter re-emits each line with
// CRLF and appends the "." terminator at EOF, which is the exact byte stream the
// decoder expects. Leading dots are deliberately NOT re-stuffed — bodyReader
// already unstuffed them, and rapidyenc escapes a leading "." at encode time so
// a data line can never be mistaken for the terminator.
// Troubleshooting: every usenet segment failing with "yEnc decode ...:
// unexpected EOF"; usenet downloads never completing.
// Review if: Tensai75/nntp gains a raw-body accessor, or fetchSegment stops
// using nntp.Conn.Body().
type articleWireReader struct {
	br   *bufio.Reader
	buf  bytes.Buffer
	done bool
}

func newArticleWireReader(r io.Reader) *articleWireReader {
	return &articleWireReader{br: bufio.NewReader(r)}
}

func (a *articleWireReader) Read(p []byte) (int, error) {
	for a.buf.Len() == 0 {
		if a.done {
			return 0, io.EOF
		}
		line, err := a.br.ReadBytes('\n')
		if len(line) > 0 {
			// TrimRight both bytes: bodyReader only rewrites CRLF when the line
			// actually had one, so a wire line that was already bare-LF passes
			// through untouched.
			a.buf.Write(bytes.TrimRight(line, "\r\n"))
			a.buf.WriteString("\r\n")
		}
		if err != nil {
			// bodyReader stops at the article-terminating "." line and reports
			// io.EOF, so any error here means the article ended. Re-append the
			// terminator exactly once.
			a.done = true
			a.buf.WriteString(".\r\n")
		}
	}
	return a.buf.Read(p)
}

// TestConnect dials, authenticates (if credentials are set), and immediately
// disconnects. Returns nil on success. Used by the Settings "Test connection"
// button to validate an NNTP server config without starting the full engine.
func TestConnect(cfg ServerConfig) error {
	p := newPool(cfg)
	c, err := p.dial()
	if err != nil {
		return err
	}
	c.Quit()
	return nil
}

// ParseURL parses a "nntp://..." or "nntps://..." URL into a ServerConfig.
// Missing port defaults to 563 (TLS) or 119 (plain).
func ParseURL(rawURL string) (ServerConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("usenet: invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "nntp" && u.Scheme != "nntps" {
		return ServerConfig{}, fmt.Errorf("usenet: URL scheme must be nntp or nntps, got %q", u.Scheme)
	}
	tls := u.Scheme == "nntps"
	host := u.Hostname()
	port := 119
	if tls {
		port = 563
	}
	if portStr := u.Port(); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("usenet: invalid port in URL %q: %w", rawURL, err)
		}
		port = p
	}
	return ServerConfig{Host: host, Port: port, TLS: tls}, nil
}

// mapNNTPError translates NNTP protocol error codes 430 and 451 to named
// sentinel errors; other errors pass through unchanged.
func mapNNTPError(err error) error {
	var e nntp.Error
	if errors.As(err, &e) {
		switch e.Code {
		case 430:
			return ErrArticleNotFound
		case 451:
			return ErrArticleRemoved
		}
	}
	return err
}
