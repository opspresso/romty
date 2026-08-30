// Roots, workspaces and tabs: the operations that change what the daemon
// remembers, and the snapshot it hands back afterwards.

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

func (s *Server) snapshotResponse() protocol.Response {
	snapshot := s.snapshot()
	return protocol.Response{Snapshot: &snapshot}
}

// removeRoot forgets a root and everything under it. Without it a root that
// became unreadable could only be dropped by editing the state file by hand.
// Its directory stays on disk, but its terminal sessions are terminated so
// they do not survive as processes the workspace tree can no longer reach.
func (s *Server) removeRoot(rootID string) protocol.Response {
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: errShuttingDown}
	}

	s.mu.Lock()
	index := -1
	for position, root := range s.value.Roots {
		if root.ID == rootID {
			index = position
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: errRootNotFound}
	}

	previous := cloneState(s.value)
	s.value.Roots = append(s.value.Roots[:index], s.value.Roots[index+1:]...)
	sessions, _ := s.dropWorkspacesLocked(func(workspace model.Workspace) bool {
		return workspace.RootID == rootID
	})
	if err := s.saveLocked(previous); err != nil {
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: err.Error()}
	}
	s.mu.Unlock()
	closeSessions(sessions)
	finish()
	return s.snapshotResponse()
}

// dropWorkspacesLocked forgets every workspace the predicate names along with
// the tabs inside them, and hands back the sessions those tabs were running
// and how many workspaces went. The caller closes the sessions once the lock
// is released: closing a shell waits for it to be reaped, and waiting for that
// under the lock stops every other request the daemon is serving.
//
// Forgetting a root and deleting a workspace directory differ only in which
// workspaces they name. Everything after that was written out twice.
func (s *Server) dropWorkspacesLocked(matches func(model.Workspace) bool) ([]*session, int) {
	orphaned := make(map[string]struct{})
	workspaces := make([]model.Workspace, 0, len(s.value.Workspaces))
	for _, workspace := range s.value.Workspaces {
		if matches(workspace) {
			orphaned[workspace.ID] = struct{}{}
			continue
		}
		workspaces = append(workspaces, workspace)
	}
	s.value.Workspaces = workspaces
	sessions := make([]*session, 0, len(orphaned))
	tabs := make([]model.Tab, 0, len(s.value.Tabs))
	for _, tab := range s.value.Tabs {
		if _, removed := orphaned[tab.WorkspaceID]; removed {
			if value, ok := s.sessions[tab.ID]; ok {
				sessions = append(sessions, value)
			}
			continue
		}
		tabs = append(tabs, tab)
	}
	s.value.Tabs = tabs
	return sessions, len(orphaned)
}

// saveLocked records the state and, when the write fails, puts back what was
// there before it. An in-memory tree that outran the file it is supposed to be
// recorded in is the one outcome none of these operations can leave behind:
// the next daemon would start from the file. The server lock must be held.
func (s *Server) saveLocked(previous model.State) error {
	if err := s.store.Save(s.value); err != nil {
		s.value = previous
		return err
	}
	s.revision++
	return nil
}

func closeSessions(sessions []*session) {
	for _, value := range sessions {
		value.close()
	}
}

func (s *Server) removeWorkspace(rootID, path string) protocol.Response {
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: errShuttingDown}
	}
	defer finish()

	s.mu.Lock()
	root, ok := findRoot(s.value.Roots, rootID)
	s.mu.Unlock()
	if !ok {
		return protocol.Response{Error: errRootNotFound}
	}
	workspacePath, err := removableWorkspacePath(root, path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	if err := os.RemoveAll(workspacePath); err != nil {
		return protocol.Response{Error: fmt.Sprintf("delete workspace: %v", err)}
	}

	s.mu.Lock()
	previous := cloneState(s.value)
	sessions, removed := s.dropWorkspacesLocked(func(workspace model.Workspace) bool {
		return workspace.RootID == rootID && workspace.Path == workspacePath
	})
	if removed == 0 {
		// A directory romty was never asked to open has no record to rewrite,
		// and rewriting one anyway would let a failed write turn a deletion
		// that succeeded into an error. The tree still changed, so the
		// revision moves for the directory that is no longer on disk.
		s.revision++
	} else if err := s.saveLocked(previous); err != nil {
		s.mu.Unlock()
		return protocol.Response{Error: fmt.Sprintf("workspace directory deleted but persist state: %v", err)}
	}
	s.mu.Unlock()
	closeSessions(sessions)
	return s.snapshotResponse()
}

func removableWorkspacePath(root model.Root, path string) (string, error) {
	workspacePath := filepath.Clean(path)
	if path == "" || workspacePath != path || workspacePath == root.Path || filepath.Dir(workspacePath) != root.Path {
		return "", fmt.Errorf("workspace must be a direct child of its root")
	}
	canonicalRoot, err := canonicalDirectory(root.Path)
	if err != nil {
		return "", fmt.Errorf("inspect workspace root: %w", err)
	}
	if canonicalRoot != root.Path {
		return "", fmt.Errorf("workspace root changed since it was registered")
	}
	info, err := os.Lstat(workspacePath)
	if errors.Is(err, os.ErrNotExist) {
		// Nothing on disk to delete, but the record is still romty's to
		// forget. Refusing here is what could trap a workspace for good: a
		// removal whose directory went but whose state could not be written
		// rolls the record back, and every retry then died on this check with
		// "no such file or directory" — leaving a workspace in the tree that
		// named nothing and that no action could remove.
		return workspacePath, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}
	return workspacePath, nil
}

func (s *Server) addRoot(path string) protocol.Response {
	canonical, err := resolveDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: errShuttingDown}
	}

	s.mu.Lock()
	for _, root := range s.value.Roots {
		if root.Path == canonical {
			s.mu.Unlock()
			finish()
			return s.snapshotResponse()
		}
	}
	previous := cloneState(s.value)
	root := model.Root{ID: newID(), Name: filepath.Base(canonical), Path: canonical}
	s.value.Roots = append(s.value.Roots, root)
	if err := s.saveLocked(previous); err != nil {
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: err.Error()}
	}
	s.mu.Unlock()
	finish()
	return s.snapshotResponse()
}

func (s *Server) ensureWorkspace(rootID, path string) protocol.Response {
	s.mu.Lock()
	if _, ok := findRoot(s.value.Roots, rootID); !ok {
		s.mu.Unlock()
		return protocol.Response{Error: errRootNotFound}
	}
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID && workspace.Path == path && workspaceHasTabs(s.value.Tabs, workspace.ID) {
			copy := workspace
			s.mu.Unlock()
			return protocol.Response{Workspace: &copy}
		}
	}
	s.mu.Unlock()

	canonical, err := resolveDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: errShuttingDown}
	}
	defer finish()

	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := findRoot(s.value.Roots, rootID)
	if !ok {
		return protocol.Response{Error: errRootNotFound}
	}
	if canonical != root.Path && filepath.Dir(canonical) != root.Path {
		return protocol.Response{Error: "workspace must be its root or a direct child"}
	}
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID && workspace.Path == canonical {
			copy := workspace
			return protocol.Response{Workspace: &copy}
		}
	}

	workspace := model.Workspace{
		ID:     newID(),
		RootID: rootID,
		Name:   filepath.Base(canonical),
		Path:   canonical,
	}
	previous := cloneState(s.value)
	s.value.Workspaces = append(s.value.Workspaces, workspace)
	if err := s.saveLocked(previous); err != nil {
		return protocol.Response{Error: err.Error()}
	}
	return protocol.Response{Workspace: &workspace}
}

func (s *Server) createTab(request protocol.Request) protocol.Response {
	workspaceID := request.WorkspaceID
	shell := s.shell
	if request.Shell != "" {
		shell = request.Shell
	}

	s.mu.Lock()
	workspace, ok := findWorkspace(s.value.Workspaces, workspaceID)
	if !ok {
		s.mu.Unlock()
		return protocol.Response{Error: "workspace not found"}
	}
	tab := model.Tab{
		ID:          newID(),
		WorkspaceID: workspaceID,
		Name:        nextTabName(s.value.Tabs, workspaceID),
		Running:     true,
	}

	// The shell is started, registered and persisted without releasing the
	// lock. startSession begins waiting on the process immediately, so a shell
	// that exits at once calls sessionExited straight away; holding the lock
	// makes that wait until the tab exists, rather than removing a tab that is
	// not there yet and leaving a dead one behind afterwards.
	value, err := startSession(tab.ID, workspace.Path, shell, request.Environment,
		request.Columns, request.Rows, func() {
			s.sessionExited(tab.ID)
		})
	if err != nil {
		s.mu.Unlock()
		return protocol.Response{Error: err.Error()}
	}
	previous := cloneState(s.value)
	s.value.Tabs = append(s.value.Tabs, tab)
	s.sessions[tab.ID] = value
	if err := s.saveLocked(previous); err != nil {
		delete(s.sessions, tab.ID)
		s.mu.Unlock()
		value.close()
		return protocol.Response{Error: err.Error()}
	}
	s.mu.Unlock()
	// Before the reply, so the attach that follows it replays the restored
	// output rather than racing it. The stopping daemon saved by workspace;
	// each new tab here consumes one snapshot, oldest tab name first.
	if meta, recording, ok := s.resume.take(workspaceID); ok {
		value.restore(recording, resumeCommand(meta.Agent, meta.AgentSessionID))
	}
	return protocol.Response{Tab: &tab}
}

// snapshot never fails: a root romty cannot read is reported as one unreadable
// root rather than taken out of the tree, because one unmounted volume used to
// fail every refresh, and with it every path that needs a snapshot.
func (s *Server) snapshot() model.Snapshot {
	s.mu.Lock()
	value := cloneState(s.value)
	revision := s.revision
	s.mu.Unlock()
	statuses := s.agentStatusesSnapshot()
	for index := range value.Tabs {
		status := statuses[value.Tabs[index].ID]
		value.Tabs[index].Agent = status.Agent
		value.Tabs[index].AgentPhase = status.Phase
		value.Tabs[index].AgentContextTokens = status.ContextTokens
		value.Tabs[index].AgentCostUSD = status.CostUSD
	}

	result := model.Snapshot{Revision: revision, Roots: make([]model.RootView, 0, len(value.Roots))}
	for _, root := range value.Roots {
		rootWorkspace, _ := workspaceAt(value.Workspaces, root.ID, root.Path)
		entries, err := readDirectory(root.Path)
		if err != nil {
			directories := appendMissingRunningWorkspaces(nil, value, root)
			result.Roots = append(result.Roots, model.RootView{
				Root:        root,
				Tabs:        tabsFor(value.Tabs, rootWorkspace.ID),
				Error:       err.Error(),
				Directories: directories,
			})
			continue
		}
		directories := make([]model.WorkspaceView, 0)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root.Path, entry.Name())
			workspace, ok := workspaceAt(value.Workspaces, root.ID, path)
			if !ok {
				workspace = model.Workspace{RootID: root.ID, Name: entry.Name(), Path: path}
			}
			directories = append(directories, model.WorkspaceView{
				Workspace: workspace,
				Tabs:      tabsFor(value.Tabs, workspace.ID),
			})
		}
		directories = appendMissingRunningWorkspaces(directories, value, root)
		sort.Slice(directories, func(i, j int) bool {
			return directories[i].Workspace.Name < directories[j].Workspace.Name
		})
		result.Roots = append(result.Roots, model.RootView{
			Root:        root,
			Tabs:        tabsFor(value.Tabs, rootWorkspace.ID),
			Directories: directories,
		})
	}
	return result
}

func appendMissingRunningWorkspaces(directories []model.WorkspaceView, value model.State, root model.Root) []model.WorkspaceView {
	seen := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		seen[directory.Workspace.Path] = struct{}{}
	}
	for _, workspace := range value.Workspaces {
		if workspace.RootID != root.ID || workspace.Path == root.Path {
			continue
		}
		if _, ok := seen[workspace.Path]; ok {
			continue
		}
		tabs := tabsFor(value.Tabs, workspace.ID)
		if len(tabs) == 0 {
			continue
		}
		directories = append(directories, model.WorkspaceView{Workspace: workspace, Tabs: tabs})
		seen[workspace.Path] = struct{}{}
	}
	return directories
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve directory symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func newID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		panic(fmt.Sprintf("generate identifier: %v", err))
	}
	return hex.EncodeToString(data)
}

func findRoot(values []model.Root, id string) (model.Root, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return model.Root{}, false
}

func findWorkspace(values []model.Workspace, id string) (model.Workspace, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return model.Workspace{}, false
}

func workspaceAt(values []model.Workspace, rootID, path string) (model.Workspace, bool) {
	for _, value := range values {
		if value.RootID == rootID && value.Path == path {
			return value, true
		}
	}
	return model.Workspace{}, false
}

func tabsFor(values []model.Tab, workspaceID string) []model.Tab {
	result := make([]model.Tab, 0)
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			result = append(result, value)
		}
	}
	return result
}

func workspaceHasTabs(values []model.Tab, workspaceID string) bool {
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			return true
		}
	}
	return false
}

func nextTabName(values []model.Tab, workspaceID string) string {
	names := make(map[string]struct{})
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			names[value.Name] = struct{}{}
		}
	}
	for number := 1; ; number++ {
		name := strconv.Itoa(number)
		if _, exists := names[name]; !exists {
			return name
		}
	}
}

func cloneState(value model.State) model.State {
	return model.State{
		Roots:      append([]model.Root(nil), value.Roots...),
		Workspaces: append([]model.Workspace(nil), value.Workspaces...),
		Tabs:       append([]model.Tab(nil), value.Tabs...),
	}
}
