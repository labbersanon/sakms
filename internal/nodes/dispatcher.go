package nodes

import (
	"context"
	"crypto/rand"
	"time"
)

// LocalHasher is the local fallback the Dispatcher wraps: the same
// Hash(ctx, path) (string, error) contract *phash.Hasher and *videophash.Hasher
// already satisfy.
type LocalHasher interface {
	Hash(context.Context, string) (string, error)
}

// Dispatcher bridges the synchronous PHasher interface (called inside Scan
// loops) to asynchronous SSE dispatch. Its Hash implements
// Hash(ctx, path) (string, error), so it satisfies both dedup.PHasher and
// rename.PHasher, letting it wrap the local hasher at the injection point with
// no downstream signature change. Every path falls back to the local hasher.
type Dispatcher struct {
	reg     *Registry
	jobType JobType
	local   LocalHasher
	timeout time.Duration // per-job wait before falling back to local
}

// NewDispatcher builds a Dispatcher for one job type. timeout is the per-job
// wait before falling back to local execution (and, on a true timeout, driving
// the node's circuit breaker).
func NewDispatcher(reg *Registry, jobType JobType, local LocalHasher, timeout time.Duration) *Dispatcher {
	return &Dispatcher{reg: reg, jobType: jobType, local: local, timeout: timeout}
}

// Hash dispatches the file to an eligible node and waits for its result,
// falling back to the local hasher on every non-success path: no eligible node,
// node-reported error, per-job timeout (which also drives the circuit breaker),
// and operator ctx-cancel (which does NOT penalise the node). A successful node
// result resets the node's circuit breaker via noteSuccess.
func (d *Dispatcher) Hash(ctx context.Context, path string) (string, error) {
	job := Job{ID: rand.Text(), Type: d.jobType, ServerPath: path}

	nodeID, result, ok := d.reg.Dispatch(job)
	if !ok {
		return d.local.Hash(ctx, path)
	}
	defer d.reg.ClearPending(job.ID)

	select {
	case res := <-result:
		if res.Error != "" {
			return d.local.Hash(ctx, path) // node reported a hash error
		}
		d.reg.noteSuccess(nodeID)
		return res.Hash, nil
	case <-time.After(d.timeout):
		d.reg.noteTimeout(nodeID) // true timeout — drives the circuit breaker
		return d.local.Hash(ctx, path)
	case <-ctx.Done():
		return d.local.Hash(ctx, path) // operator cancel — do NOT penalise the node
	}
}
