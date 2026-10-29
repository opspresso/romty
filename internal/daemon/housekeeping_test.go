package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

// Several clients asking at once share one scan, and a change clears it.
func TestAgentStatusScansAreSharedUntilAChange(t *testing.T) {
	testutil.Set(t, &agentStatusCacheTTL, time.Minute)
	server, err := New(filepath.Join(testutil.ShortTempDir(t), "s.sock"), filepath.Join(t.TempDir(), "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.agentStatusesSnapshot()
	server.statusCache["marker"] = model.AgentStatus{Agent: model.AgentClaude}
	if _, cached := server.agentStatusesSnapshot()["marker"]; !cached {
		t.Fatal("a second request inside the TTL scanned again")
	}
	server.invalidateAgentStatuses()
	if _, cached := server.agentStatusesSnapshot()["marker"]; cached {
		t.Fatal("an invalidated cache answered a request")
	}
}

// A background job of a closed tab is ended with it, not left running.
func TestClosingASessionEndsItsBackgroundJobs(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "job.pid")
	value, err := startSession("tab", directory, "/bin/sh", nil, 80, 24, func() {})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	// A job that ignores SIGHUP, the way nohup leaves one, in the background.
	if _, err := value.pty.WriteString("trap '' HUP; (trap '' HUP; sleep 300) & echo $! > " + marker + "\n"); err != nil {
		t.Fatalf("write command: %v", err)
	}
	var pid string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) != "" {
			pid = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == "" {
		value.close()
		t.Fatal("the background job did not start")
	}
	value.close()
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		_ = exec.Command("kill", "-9", pid).Run()
		t.Fatalf("background job %s outlived its tab", pid)
	}
}

// The quietest tabs give up their recordings first once the budget is spent.
func TestRecordingBudgetTrimsTheQuietestTabs(t *testing.T) {
	testutil.Set(t, &recordingBudget, 3<<10)
	testutil.Set(t, &idleRecordingBytes, 1<<10)
	server := &Server{sessions: make(map[string]*session)}
	now := time.Now()
	for index, name := range []string{"quiet", "busy"} {
		value := &session{lastOutputAt: now.Add(time.Duration(index) * time.Minute)}
		value.history.reshape(2 << 10)
		value.history.append(make([]byte, 2<<10))
		server.sessions[name] = value
	}
	server.enforceRecordingBudget()
	if got := server.sessions["quiet"].recordingCapacity(); got != 1<<10 {
		t.Fatalf("quiet tab holds %d bytes, want it trimmed to %d", got, 1<<10)
	}
	if got := server.sessions["busy"].recordingCapacity(); got != 2<<10 {
		t.Fatalf("busy tab holds %d bytes, want it kept", got)
	}
}

// allow_sleep in config.json turns the keep-awake hold off; a missing file
// keeps it on.
func TestKeepAwakeFollowsTheConfig(t *testing.T) {
	directory := t.TempDir()
	server := &Server{configPath: filepath.Join(directory, "config.json"), logger: testutil.QuietLogger()}
	if server.sleepAllowed() {
		t.Fatal("a missing config allowed sleep")
	}
	if err := os.WriteFile(server.configPath, []byte(`{"allow_sleep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !server.sleepAllowed() {
		t.Fatal("allow_sleep was ignored")
	}
}
