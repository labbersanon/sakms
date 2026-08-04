package db

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOpen_emptyDSN(t *testing.T) {
	_, err := Open(context.Background(), "", DefaultPoolOptions())
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty DSN error, got %v", err)
	}
}

func TestOpen_poolSettings(t *testing.T) {
	dsn := os.Getenv("SAKMS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SAKMS_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sqlDB, err := Open(ctx, dsn, PoolOptions{MaxOpen: 7, MaxIdle: 3})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if got := sqlDB.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections=%d want 7", got)
	}
}

func TestCmdSakms_DoesNotImportModerncSQLite(t *testing.T) {
	root := findModuleRoot(t)
	cmd := exec.Command("go", "list", "-deps", "./cmd/sakms")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "modernc.org/sqlite") {
		t.Fatal("cmd/sakms must not depend on modernc.org/sqlite after Postgres cutover")
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		i := strings.LastIndex(dir, "/")
		if i <= 0 {
			t.Fatal("go.mod not found")
		}
		dir = dir[:i]
	}
}
