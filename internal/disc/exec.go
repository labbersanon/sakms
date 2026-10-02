package disc

import (
	"context"
	"os/exec"
)

// lookPath / runCmd are swappable in tests.
var lookPath = exec.LookPath

var runCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
