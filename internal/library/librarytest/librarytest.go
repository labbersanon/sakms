// Package librarytest builds an isolated library.Store for unit tests.
// It lives outside package library so production library code does not import testing.
package librarytest

import (
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
)

func New(t *testing.T) *library.Store {
	t.Helper()
	return library.New(dbtest.New(t))
}
