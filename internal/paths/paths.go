package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	Directory string
	Socket    string
	State     string
	Log       string
}

func Resolve() (Paths, error) {
	directory := os.Getenv("ROMTY_HOME")
	if directory == "" {
		configDirectory, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		directory = filepath.Join(configDirectory, "romty")
	}

	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve romty directory: %w", err)
	}
	return Paths{
		Directory: absolute,
		Socket:    filepath.Join(absolute, "daemon.sock"),
		State:     filepath.Join(absolute, "state.json"),
		Log:       filepath.Join(absolute, "daemon.log"),
	}, nil
}

func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Directory, 0o700); err != nil {
		return fmt.Errorf("create romty directory: %w", err)
	}
	return nil
}
