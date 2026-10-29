package model

// GitState is the branch line romty shows for a repository.
type GitState struct {
	Branch     string
	Revision   string
	Detached   bool
	Dirty      bool
	Conflicted bool
	Ahead      int
	Behind     int
}

// Event is what the daemon sends a subscribed client when something it shows
// changes. Each field is set only when that part changed; a nil field means
// unchanged, and GitStates, when set, is the whole map.
type Event struct {
	Snapshot      *Snapshot              `json:"snapshot,omitempty"`
	AgentStatuses map[string]AgentStatus `json:"agent_statuses,omitempty"`
	GitStates     map[string]GitState    `json:"git_states,omitempty"`
}

// EventStream is an open subscription to the daemon's events.
type EventStream interface {
	// Next waits for the next event and returns an error once the stream
	// has ended.
	Next() (Event, error)
	Close() error
}
