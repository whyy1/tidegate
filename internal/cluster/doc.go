// Package cluster implements the protocol between the controller and its
// load generator nodes.
//
// # Transport
//
// A node dials the controller and opens one gRPC bidirectional stream,
// /tidegate.cluster.v1.Cluster/Connect, described by cluster.proto. The
// message types in this package are hand-written against that schema with
// google.golang.org/protobuf/encoding/protowire, so building needs no protoc
// and generated code could replace them without changing the wire format.
// A private codec (reported as "proto") is installed on both ends; unknown
// fields are skipped.
//
// Plaintext is used unless ServerOptions.Credentials and
// ClientOptions.Credentials are set, which is where mutual TLS plugs in.
//
// # Handshake
//
// The node sends Hello. The controller checks the token (constant time, see
// TokenEqual) and then requires Hello.Version to equal its own version. On
// failure it sends Reject with a reason code and a Simplified Chinese
// message, then ends the stream; Client.Run returns a *RejectedError and
// does not retry. On success it sends Welcome with the node ID, its clock
// and its ping interval. A reconnecting node sends its previous ID in Hello
// and keeps it; if a stream with that ID is still open, the old stream is
// closed (OnNodeLeft with ErrNodeReplaced) before the new one joins.
//
// # Liveness
//
// gRPC keepalive detects dead transports in both directions. In addition the
// controller sends Ping every ServerOptions.PingInterval and drops a node
// when nothing at all (Pong, SysStats or anything else) arrived from it for
// ServerOptions.PingTimeout. Server events for a node are delivered in order
// and OnNodeLeft is delivered exactly once.
//
// # Queues
//
// Controller side: each node has an outbound queue of
// ServerOptions.SendQueue messages (default 256) drained by its own
// goroutine, so a slow node never blocks sends to other nodes. Sends never
// block: when the queue is full the call returns ErrSendQueueFull, nothing is
// sent, and the node stays connected (a node that stops reading is dropped
// by the liveness check once it stops sending as well). Pings bypass the
// queue through a one-slot channel and are dropped when one is already
// pending.
//
// Node side: Pong first, then SysStats, then run reports. Only the newest
// unsent SysStats is kept, and SysStats are dropped while disconnected.
// RunSnapshot and RunFinished share a FIFO that survives reconnects, bounded
// by ClientOptions.MaxBufferedReports (default 1024) and MaxBufferedBytes
// (default 64 MiB); on overflow the oldest RunSnapshot is dropped, and
// RunFinished is never dropped.
//
// # Message size
//
// Each encoded message is limited to ClientOptions.MaxMessageSize and
// ServerOptions.MaxMessageSize, both 64 MiB by default (gRPC's own default
// is 4 MiB). The limit is checked when a message is queued, so an oversized
// StartRun or snapshot fails with ErrMessageTooLarge instead of breaking the
// stream. Both ends should use the same limit.
package cluster
