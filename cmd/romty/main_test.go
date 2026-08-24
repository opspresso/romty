package main

import (
	"strings"
	"testing"
)

func TestRunRejectsNestedRomty(t *testing.T) {
	t.Setenv("ROMTY", "1")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "inside a romty terminal") {
		t.Fatalf("run() error = %v, want nested romty error", err)
	}
}
