// Package jsonfile reads and writes the small JSON documents romty keeps on
// disk. Both of them — the daemon's state and the TUI's config — were written
// by the same thirty-five lines, differing only in the noun in their error
// messages, which is the kind of duplicate that gets fixed in one copy only.
package jsonfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Read decodes the document at path. A file that is not there is not an error:
// romty starts with no state and no config, and both are written on first use.
func Read[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return value, nil
}

// Write replaces the document at path atomically, so a crash mid-write leaves
// the previous version rather than a truncated one. The temporary file is
// narrowed before anything is written to it, never after.
func Write(path string, value any) error {
	name := filepath.Base(path)
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create %s directory: %w", name, err)
	}
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", name, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := writeAndSync(temporary, data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", name, err)
	}
	// The rename is only durable once the directory entry is, which neither
	// copy of this used to do: a crash could lose a file that Sync reported
	// as written.
	return syncDirectory(directory)
}

func writeAndSync(file *os.File, data []byte) error {
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
