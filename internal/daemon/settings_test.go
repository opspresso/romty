package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func TestSettingsControlRetentionAndSavedOutput(t *testing.T) {
	directory := t.TempDir()
	server := &Server{configPath: filepath.Join(directory, "config.json"), logger: testutil.QuietLogger()}
	if got := server.retention(); got != defaultRetention {
		t.Fatalf("retention without a config = %v", got)
	}
	if err := os.WriteFile(server.configPath, []byte(`{"output_retention_days":2,"save_output":"off"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := server.retention(); got != 48*time.Hour {
		t.Fatalf("retention = %v, want two days", got)
	}
	if server.settings().SaveOutput != "off" {
		t.Fatal("save_output was not read")
	}
	if err := os.WriteFile(server.configPath, []byte(`{"output_retention_days":400}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := server.retention(); got != defaultRetention {
		t.Fatalf("an out-of-range retention = %v, want the default", got)
	}
}

// With save_output off a saved session keeps its agent resume metadata and
// no output.
func TestSaveOutputOffKeepsTheResumeCommandOnly(t *testing.T) {
	directory := t.TempDir()
	server := &Server{configPath: filepath.Join(directory, "config.json"), logger: testutil.QuietLogger(),
		agentStatuses: map[string]agentRuntime{"t1": {AgentStatus: model.AgentStatus{Agent: model.AgentClaude, SessionID: "0123456789abcdef"}}}}
	if err := os.WriteFile(server.configPath, []byte(`{"save_output":"off"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value := &session{}
	value.history.append([]byte("secret=hunter2"))
	save := server.resumeSaveLocked(model.Tab{ID: "t1", WorkspaceID: "w", Name: "1"}, value, time.Now())
	if output := save.recording(); len(output) != 0 {
		t.Fatalf("saved output = %q, want none", output)
	}
	if save.meta.Agent != model.AgentClaude || save.meta.AgentSessionID == "" {
		t.Fatalf("saved metadata = %+v, want the agent session", save.meta)
	}
}

// A crash leaves checkpoints; the next daemon offers them unless a stop saved
// the same tab more recently, and empties the checkpoint store.
func TestCheckpointsArePromotedOnTheNextStart(t *testing.T) {
	directory := t.TempDir()
	resume := &resumeStore{directory: filepath.Join(directory, "resume")}
	checkpoint := &resumeStore{directory: filepath.Join(directory, "checkpoint")}
	now := time.Now()
	for _, id := range []string{"a1", "b2"} {
		if err := checkpoint.save(id, resumeSnapshot{Format: 1, WorkspaceID: "w", TabName: id, SavedAt: now}, []byte("old "+id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := resume.save("b2", resumeSnapshot{Format: 1, WorkspaceID: "w", TabName: "b2", SavedAt: now}, []byte("stopped")); err != nil {
		t.Fatal(err)
	}
	resume.promote(checkpoint)
	if _, err := os.Stat(checkpoint.directory); !os.IsNotExist(err) {
		t.Fatal("the checkpoint store was not emptied")
	}
	if _, recording, err := resume.read("w", "a1"); err != nil || string(recording) != "old a1" {
		t.Fatalf("promoted a1 = (%q, %v)", recording, err)
	}
	if _, recording, _ := resume.read("w", "b2"); string(recording) != "stopped" {
		t.Fatalf("b2 = %q, want the stop's newer snapshot kept", recording)
	}
}

// Checkpoints cover agent tabs only.
func TestCheckpointAgentsSavesAgentTabsOnly(t *testing.T) {
	directory := t.TempDir()
	server := &Server{
		logger:        testutil.QuietLogger(),
		checkpoint:    &resumeStore{directory: filepath.Join(directory, "checkpoint")},
		value:         model.State{Tabs: []model.Tab{{ID: "a1", WorkspaceID: "w"}, {ID: "b2", WorkspaceID: "w"}}},
		sessions:      map[string]*session{"a1": {}, "b2": {}},
		agentStatuses: map[string]agentRuntime{"a1": {AgentStatus: model.AgentStatus{Agent: model.AgentClaude}}},
	}
	server.checkpointAgents()
	if _, _, err := server.checkpoint.read("w", "a1"); err != nil {
		t.Fatal("the agent tab was not checkpointed")
	}
	if _, _, err := server.checkpoint.read("w", "b2"); err == nil {
		t.Fatal("a plain shell was checkpointed")
	}
}
