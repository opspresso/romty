package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
)

func TestResumeStoreTakesNewestStopThenLowestTabName(t *testing.T) {
	store := &resumeStore{directory: t.TempDir()}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	for _, save := range []struct {
		id      string
		name    string
		savedAt time.Time
	}{
		{id: "tab-old", name: "1", savedAt: older},
		{id: "tab-two", name: "2", savedAt: newer},
		{id: "tab-one", name: "1", savedAt: newer},
	} {
		meta := resumeSnapshot{
			Format: 1, WorkspaceID: "workspace-1", TabName: save.name, SavedAt: save.savedAt,
		}
		if err := store.save(save.id, meta, []byte("output "+save.id)); err != nil {
			t.Fatalf("save(%s) error = %v", save.id, err)
		}
	}
	if err := store.save("tab-other", resumeSnapshot{
		Format: 1, WorkspaceID: "workspace-2", TabName: "1", SavedAt: newer,
	}, []byte("other")); err != nil {
		t.Fatalf("save(tab-other) error = %v", err)
	}

	wants := []struct {
		name      string
		recording string
	}{
		// The newest stop first, and inside it the lowest tab name; the older
		// generation only after the newer one is used up.
		{name: "1", recording: "output tab-one"},
		{name: "2", recording: "output tab-two"},
		{name: "1", recording: "output tab-old"},
	}
	for index, want := range wants {
		meta, recording, ok := store.take("workspace-1")
		if !ok {
			t.Fatalf("take() #%d found nothing", index)
		}
		if meta.TabName != want.name || string(recording) != want.recording {
			t.Fatalf("take() #%d = (%q, %q), want (%q, %q)",
				index, meta.TabName, recording, want.name, want.recording)
		}
	}
	if _, _, ok := store.take("workspace-1"); ok {
		t.Fatal("take() found a snapshot after every one was consumed")
	}
	if _, _, ok := store.take("workspace-2"); !ok {
		t.Fatal("take() consumed another workspace's snapshot")
	}
}

func TestResumeStoreDiscardsStaleSnapshots(t *testing.T) {
	directory := t.TempDir()
	store := &resumeStore{directory: directory}
	if err := store.save("tab-1", resumeSnapshot{
		Format: 1, WorkspaceID: "workspace-1", TabName: "1", SavedAt: time.Now(),
	}, []byte("output")); err != nil {
		t.Fatalf("save() error = %v", err)
	}

	store.discardStale(time.Now())
	if _, _, ok := store.take("workspace-1"); !ok {
		t.Fatal("discardStale removed a fresh snapshot")
	}

	if err := store.save("tab-1", resumeSnapshot{
		Format: 1, WorkspaceID: "workspace-1", TabName: "1", SavedAt: time.Now(),
	}, []byte("output")); err != nil {
		t.Fatalf("save() again error = %v", err)
	}
	store.discardStale(time.Now().Add(resumeRetention + time.Hour))
	if _, _, ok := store.take("workspace-1"); ok {
		t.Fatal("discardStale kept a snapshot past its retention")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("resume directory still holds %d files", len(entries))
	}
}

func TestResumeStoreSkipsACorruptSnapshot(t *testing.T) {
	directory := t.TempDir()
	store := &resumeStore{directory: directory}
	if err := os.WriteFile(filepath.Join(directory, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := store.save("tab-1", resumeSnapshot{
		Format: 1, WorkspaceID: "workspace-1", TabName: "1", SavedAt: time.Now(),
	}, []byte("output")); err != nil {
		t.Fatalf("save() error = %v", err)
	}
	if _, _, ok := store.take("workspace-1"); !ok {
		t.Fatal("a corrupt snapshot kept the workspace from restoring")
	}
}

// The command is typed into a shell, so only an identifier shaped like the
// ones the agents actually report may be interpolated; anything else falls
// back to the agent's own "most recent session" form.
func TestResumeCommandInterpolatesOnlyWellFormedSessionIDs(t *testing.T) {
	for _, probe := range []struct {
		name      string
		agent     model.Agent
		sessionID string
		want      string
	}{
		{name: "claude with a session", agent: model.AgentClaude,
			sessionID: "0123abcd-89ef-4567-0123-456789abcdef",
			want:      "claude --resume 0123abcd-89ef-4567-0123-456789abcdef"},
		{name: "claude without a session", agent: model.AgentClaude,
			want: "claude --continue"},
		{name: "claude with a hostile session", agent: model.AgentClaude,
			sessionID: "x; rm -rf ~",
			want:      "claude --continue"},
		{name: "codex with a session", agent: model.AgentCodex,
			sessionID: "0123abcd-89ef-4567-0123-456789abcdef",
			want:      "codex resume 0123abcd-89ef-4567-0123-456789abcdef"},
		{name: "codex without a session", agent: model.AgentCodex,
			want: "codex resume"},
		{name: "no agent", want: ""},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := resumeCommand(probe.agent, probe.sessionID); got != probe.want {
				t.Fatalf("resumeCommand(%q, %q) = %q, want %q",
					probe.agent, probe.sessionID, got, probe.want)
			}
		})
	}
}
