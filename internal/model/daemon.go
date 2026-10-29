package model

import "errors"

// ErrDaemonUnavailable is what a request returns when no daemon answers on
// the socket. The client adapter wraps the system error in it, so a caller
// can tell a stopped daemon from a failed request without knowing sockets.
var ErrDaemonUnavailable = errors.New("romty daemon is not running")

// ErrNeedsNewerDaemon is what a request for a feature the running daemon
// predates returns, so a caller can fall back to an older way.
var ErrNeedsNewerDaemon = errors.New("the running romty daemon is too old for this")

// RefusedError is a request the daemon answered with an error. Message is
// the daemon's reason as it wrote it, for a caller that shows the reason
// beside the request and needs no "daemon:" prefix.
type RefusedError struct {
	Message string
}

func (e *RefusedError) Error() string { return "daemon: " + e.Message }

// DaemonInfo is what the running daemon says about itself.
type DaemonInfo struct {
	// Version is the daemon's build, or empty for a daemon that predates
	// reporting it, which is older than every client that asks.
	Version        string
	RecordingBytes int64
}
