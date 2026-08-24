package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nalbam/romty/internal/client"
	"github.com/nalbam/romty/internal/daemon"
	"github.com/nalbam/romty/internal/paths"
)

func TestRunRejectsNestedRomty(t *testing.T) {
	t.Setenv("ROMTY", "1")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "inside a romty terminal") {
		t.Fatalf("run() error = %v, want nested romty error", err)
	}
}

func TestRunStopsDaemon(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-main-test-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	t.Setenv("ROMTY", "")
	t.Setenv("ROMTY_HOME", base)
	runtime, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	server, err := daemon.New(runtime.Socket, runtime.State, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	backend := client.New(runtime.Socket)
	deadline := time.Now().Add(3 * time.Second)
	for backend.Ping() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	originalArgs := os.Args
	os.Args = []string{"romty", "stop"}
	defer func() { os.Args = originalArgs }()

	if err := run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
