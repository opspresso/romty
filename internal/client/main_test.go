package client

import (
	"os"
	"testing"
)

// TestMain gives the tests a home of their own, so EnsureDaemon never finds
// and starts a login service installed on the machine running them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "romty-client-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
