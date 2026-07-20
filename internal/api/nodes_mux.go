package api

import (
	"net/http"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/nodekeys"
	"github.com/labbersanon/sakms/internal/nodes"
)

// NewNodesMux returns a ServeMux for all /api/nodes/* routes with per-handler
// authentication. Node-agent routes (stream, heartbeat, job result) require
// Authorization: Bearer <nodeKey>. Operator routes (list, approve, reject,
// settings) require the master API key or a valid session cookie, identical to
// the rest of the API.
//
// This mux is mounted WITHOUT wrapping it in auth.Middleware so that operator
// and node routes can each enforce their own distinct credential type. The
// unauthenticated pairing endpoint (GET /api/nodes/pair) is mounted separately
// on the top-level mux as an exact match that beats this subtree.
func NewNodesMux(
	reg *nodes.Registry,
	pairingReg *nodes.PairingRegistry,
	nodeKeyStore *nodekeys.Store,
	enc auth.TokenEncryptor,
	authStore *auth.Store,
) *http.ServeMux {
	mux := http.NewServeMux()

	// Node-agent routes — validated by per-node bearer key only.
	nodeKey := func(h http.Handler) http.Handler { return auth.NodeKeyMiddleware(nodeKeyStore, h) }
	mux.Handle("GET /api/nodes/stream", nodeKey(nodeStreamHandler(reg)))
	mux.Handle("POST /api/nodes/heartbeat", nodeKey(nodeHeartbeatHandler(reg)))
	mux.Handle("POST /api/nodes/jobs/{id}/result", nodeKey(nodeJobResultHandler(reg)))

	// Operator routes — validated by master API key or session cookie.
	op := func(h http.Handler) http.Handler { return auth.Middleware(enc, authStore, h) }
	mux.Handle("GET /api/nodes", op(listNodesHandler(reg, pairingReg)))
	mux.Handle("POST /api/nodes/{id}/approve", op(approveNodeHandler(pairingReg, nodeKeyStore)))
	mux.Handle("DELETE /api/nodes/{id}/pending", op(rejectPendingHandler(pairingReg)))
	mux.Handle("PUT /api/nodes/{id}/settings", op(updateNodeSettingsHandler(reg)))

	return mux
}
