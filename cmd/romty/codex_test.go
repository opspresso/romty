package main

import (
	"reflect"
	"testing"
)

func TestCodexBridgePreservesTheTerminalWorkingDirectory(t *testing.T) {
	for _, args := range [][]string{nil, {"resume", "session"}, {"--", "prompt"}} {
		want := append([]string{"--remote", "unix:///tmp/proxy", "--cd", "/current/workspace"}, args...)
		if got := codexArguments("/tmp/proxy", "/current/workspace", args); !reflect.DeepEqual(got, want) {
			t.Fatalf("arguments = %q, want %q", got, want)
		}
	}
	for _, args := range [][]string{{"--cd", "/selected"}, {"-C", "/selected"}, {"--cd=/selected"}, {"-C/selected"}} {
		want := append([]string{"--remote", "unix:///tmp/proxy"}, args...)
		if got := codexArguments("/tmp/proxy", "/current/workspace", args); !reflect.DeepEqual(got, want) {
			t.Fatalf("explicit directory changed: %q", got)
		}
	}
}
