package httpapi

import (
	"hash/fnv"
	"os"
	"strconv"
)

// Terminal-stream affinity.
//
// The same-replica fast path (see readTerminalInput / terminalStreams.pushInput)
// only fires when the client's terminal WebSocket and the worker's terminal
// stream are handled by the same control-plane replica. With N replicas behind
// a load balancer that routes each connection independently, that is a ~1/N
// coincidence. Affinity raises the hit rate by giving every terminal a stable
// "home" replica derived from a consistent hash of its session, so an
// affinity-aware entry can route both connections there.
//
// This file provides the deterministic building blocks — a stable routing key
// and this process's replica identity. Activating them end to end needs an
// infrastructure step that is intentionally NOT in this change: the entry
// (an ALB cannot co-locate two independent connections by a shared key) must
// consult the routing key, which in practice means either a consistent-hash
// proxy in front, or ECS Service Connect / Cloud Map so a non-home replica can
// forward to the home one. Both require a load test to tune, which is deferred.
// The fast path already captures the co-located ~1/N of traffic without it.

// replicaShardSpace is the fixed virtual-shard count the routing key spans.
// A router maps these shards onto its live replica set; keeping the space large
// and fixed lets replicas scale without rehashing every terminal.
const replicaShardSpace = 4096

// terminalRoutingKey returns a stable shard in [0, replicaShardSpace) for a
// session. Both a terminal's client socket and its worker stream hash to the
// same key, so a consistent-hash entry sends them to the same replica.
func terminalRoutingKey(sessionID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return int(h.Sum32() % replicaShardSpace)
}

// replicaID identifies this control-plane process for affinity and observability.
// It prefers an explicit AO_CLOUD_REPLICA_ID (e.g. the ECS task id), then the
// hostname, and is empty only when neither is available.
func replicaID() string {
	if id := os.Getenv("AO_CLOUD_REPLICA_ID"); id != "" {
		return id
	}
	if host, err := os.Hostname(); err == nil {
		return host
	}
	return ""
}

// routingKeyString renders the routing key for transport in API responses.
func routingKeyString(sessionID string) string {
	return strconv.Itoa(terminalRoutingKey(sessionID))
}
