package usenet

import (
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/Tensai75/nntp"
	"github.com/mnightingale/rapidyenc"
)

// ErrArticleNotFound is returned by fetchSegment when the server responds 430.
var ErrArticleNotFound = errors.New("usenet: article not found (430)")

// ErrArticleRemoved is returned by fetchSegment when the server responds 451
// (typically a DMCA takedown).
var ErrArticleRemoved = errors.New("usenet: article removed (451)")

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
	// and the limit the Manager passes to errgroup for parallel segment fetches.
	// Defaults to 4 when zero.
	MaxConns int
}

// pool recycles authenticated NNTP connections to a single server. It does not
// cap concurrency itself — the Manager limits parallel segment fetches via
// errgroup.SetLimit(MaxConns).
type pool struct {
	cfg  ServerConfig
	idle chan *nntp.Conn
}

func newPool(cfg ServerConfig) *pool {
	n := cfg.MaxConns
	if n < 1 {
		n = 4
	}
	return &pool{cfg: cfg, idle: make(chan *nntp.Conn, n)}
}

// get returns an idle authenticated connection or dials a new one.
func (p *pool) get() (*nntp.Conn, error) {
	select {
	case c := <-p.idle:
		return c, nil
	default:
		return p.dial()
	}
}

// put returns c to the idle pool. A failed connection (ok=false) is closed and
// discarded — never put a broken connection back into the pool.
func (p *pool) put(c *nntp.Conn, ok bool) {
	if !ok {
		c.Quit()
		return
	}
	select {
	case p.idle <- c:
	default:
		// Pool is full (shouldn't happen if callers respect MaxConns, but be safe).
		c.Quit()
	}
}

// close terminates all idle connections in the pool.
func (p *pool) close() {
	for {
		select {
		case c := <-p.idle:
			c.Quit()
		default:
			return
		}
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
	body, err := c.Body("<" + msgID + ">")
	if err != nil {
		return segmentResult{}, mapNNTPError(err)
	}
	// WithStatusLineAlreadyRead: nntp.Conn.Body() already consumed the
	// "222 Body follows" status line before returning the io.Reader, so the
	// decoder starts reading at the first line of the yEnc body.
	dec := rapidyenc.NewDecoder(body, rapidyenc.WithStatusLineAlreadyRead())
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
