package state_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/state"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := state.New(path)
	want := model.State{
		Roots:      []model.Root{{ID: "root-1", Name: "code", Path: "/code"}},
		Workspaces: []model.Workspace{{ID: "workspace-1", RootID: "root-1", Name: "romty", Path: "/code/romty"}},
		Tabs:       []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestStoreMissingFile(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, model.State{}) {
		t.Fatalf("Load() = %#v, want empty state", got)
	}
}
