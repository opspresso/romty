// Package sound plays the short notification sounds embedded in romty.
package sound

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type Kind string

const (
	Done    Kind = "done"
	Waiting Kind = "waiting"
)

//go:embed assets/done.wav
var doneWAV []byte

//go:embed assets/waiting.wav
var waitingWAV []byte

var findPlayer = exec.LookPath
var runPlayer = func(command *exec.Cmd) error { return command.Run() }

func Play(kind Kind) error {
	data, err := asset(kind)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "romty-"+string(kind)+"-*.wav")
	if err != nil {
		return fmt.Errorf("create sound file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write sound file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close sound file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command, err := playerCommand(ctx, runtime.GOOS, path, findPlayer)
	if err != nil {
		return err
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := runPlayer(command); err != nil {
		return fmt.Errorf("play %s sound: %w", kind, err)
	}
	return nil
}

func asset(kind Kind) ([]byte, error) {
	switch kind {
	case Done:
		return doneWAV, nil
	case Waiting:
		return waitingWAV, nil
	default:
		return nil, fmt.Errorf("unknown sound %q", kind)
	}
}

func playerCommand(ctx context.Context, goos, path string, lookup func(string) (string, error)) (*exec.Cmd, error) {
	type candidate struct {
		name string
		args []string
	}
	var candidates []candidate
	switch goos {
	case "darwin":
		candidates = []candidate{{name: "afplay", args: []string{path}}}
	case "linux":
		candidates = []candidate{
			{name: "paplay", args: []string{path}},
			{name: "pw-play", args: []string{path}},
			{name: "aplay", args: []string{"-q", path}},
			{name: "ffplay", args: []string{"-nodisp", "-autoexit", "-loglevel", "quiet", path}},
		}
	default:
		return nil, fmt.Errorf("sound playback is not supported on %s", goos)
	}
	for _, candidate := range candidates {
		executable, err := lookup(candidate.name)
		if err == nil {
			return exec.CommandContext(ctx, executable, candidate.args...), nil
		}
	}
	return nil, errors.New("no supported sound player found")
}
