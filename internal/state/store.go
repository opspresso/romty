// Package state is the daemon's record of the workspace tree, held in memory
// and written to one JSON file so the next daemon starts where this one
// stopped.
package state

import (
	"sync"

	"github.com/opspresso/romty/internal/jsonfile"
	"github.com/opspresso/romty/internal/model"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (model.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return jsonfile.Read[model.State](s.path)
}

func (s *Store) Save(value model.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return jsonfile.Write(s.path, value)
}
