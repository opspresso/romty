package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/testutil"
)

func TestServePreservesAReplacementSocketPath(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	server := newServerAt(t, socket, filepath.Join(base, "state.json"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	dialDaemon(t, socket).Close()
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "replaced by another file") {
			t.Fatalf("Serve() = %v, want socket replacement error", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("Serve() did not report the replaced socket")
	}
	content, err := os.ReadFile(socket)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement after shutdown = %q, %v", content, err)
	}
	lock, err := lockDaemon(socket + lockSuffix)
	if err != nil {
		t.Fatalf("daemon did not release its lock: %v", err)
	}
	lock.Close()
}

func TestSocketRecoveryCannotReopenAClosedListener(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "daemon.sock")
	listener, err := listenPrivately(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.restore(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("restore() after Close() = %v", err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed listener recreated its socket: %v", err)
	}
}
