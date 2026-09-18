package usenet

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
)

// TestIsTransportError_TypedErrors covers the main typed cases.
func TestIsTransportError_TypedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ErrArticleNotFound", ErrArticleNotFound, false},
		{"ErrArticleRemoved", ErrArticleRemoved, false},
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"io.EOF", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"net.ErrClosed", net.ErrClosed, true},
		{"syscall.EPIPE", syscall.EPIPE, true},
		{"syscall.ECONNRESET", syscall.ECONNRESET, true},
		{"syscall.ECONNABORTED", syscall.ECONNABORTED, true},
		{"syscall.ETIMEDOUT", syscall.ETIMEDOUT, true},
		{"syscall.ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"syscall.EHOSTUNREACH", syscall.EHOSTUNREACH, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransportError(tt.err); got != tt.want {
				t.Errorf("isTransportError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsTransportError_NetOpError covers the live broken-pipe shape.
func TestIsTransportError_NetOpError(t *testing.T) {
	// This is what "write tcp IP:PORT->IP:PORT: write: broken pipe" produces.
	err := &net.OpError{Op: "write", Err: syscall.EPIPE}
	if !isTransportError(err) {
		t.Errorf("isTransportError(*net.OpError{write,EPIPE}) = false, want true")
	}
	// Wrapped with segment context.
	wrapped := errors.New("segment 1: " + err.Error())
	// String fallback covers this case.
	if !isTransportError(wrapped) {
		t.Errorf("isTransportError(wrapped broken-pipe string) = false, want true")
	}
}

// TestIsTransportError_WrappedOpError checks that wrapping with fmt.Errorf still works.
func TestIsTransportError_WrappedOpError(t *testing.T) {
	opErr := &net.OpError{Op: "write", Err: syscall.EPIPE}
	wrapped := errors.New("usenet: yEnc decode: " + opErr.Error())
	if !isTransportError(wrapped) {
		t.Errorf("isTransportError(wrapped op error) = false, want true (string fallback)")
	}
}

// TestIsTransportError_StringFallback covers string-only transport errors.
func TestIsTransportError_StringFallback(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"write tcp 1.2.3.4:1->5.6.7.8:563: write: broken pipe", true},
		{"connection reset by peer", true},
		{"connection closed", true},
		{"use of closed network connection", true},
		{"i/o timeout", true},
		{"unexpected eof", true},
		{"no route to host", true},
		{"tls: certificate verification failed", true},
		{"usenet: article not found (430)", false},
		{"something completely different", false},
	}
	for _, c := range cases {
		err := errors.New(c.msg)
		if got := isTransportError(err); got != c.want {
			t.Errorf("isTransportError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestClassifySegmentFailure_TransportWrap verifies that errors.Is works for
// both the sentinel and the underlying cause.
func TestClassifySegmentFailure_TransportWrap(t *testing.T) {
	cause := &net.OpError{Op: "write", Err: syscall.EPIPE}
	out := classifySegmentFailure(cause)
	if !errors.Is(out, ErrTransport) {
		t.Errorf("classifySegmentFailure: errors.Is(out, ErrTransport) = false")
	}
	if !errors.Is(out, cause) {
		t.Errorf("classifySegmentFailure: errors.Is(out, cause) = false (multi-%%w not working)")
	}
}

// TestClassifySegmentFailure_NonTransportPassthrough verifies that non-transport
// errors are returned unchanged.
func TestClassifySegmentFailure_NonTransportPassthrough(t *testing.T) {
	for _, err := range []error{ErrArticleNotFound, ErrArticleRemoved, errors.New("par2: not repairable")} {
		out := classifySegmentFailure(err)
		if out != err {
			t.Errorf("classifySegmentFailure(%v): got %v, want unchanged", err, out)
		}
		if errors.Is(out, ErrTransport) {
			t.Errorf("classifySegmentFailure(%v): unexpected ErrTransport wrap", err)
		}
	}
}
