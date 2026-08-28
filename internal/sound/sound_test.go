package sound

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestEmbeddedSoundsArePCMWaveFiles(t *testing.T) {
	for kind, data := range map[Kind][]byte{Done: doneWAV, Waiting: waitingWAV} {
		if len(data) < 44 || !bytes.Equal(data[:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WAVE")) {
			t.Fatalf("%s asset is not a WAV file", kind)
		}
	}
	if bytes.Equal(doneWAV, waitingWAV) {
		t.Fatal("done and waiting sounds are identical")
	}
}

func TestPlayerCommandUsesPlatformTools(t *testing.T) {
	for _, test := range []struct {
		name      string
		goos      string
		available string
		wantArgs  []string
	}{
		{name: "macOS", goos: "darwin", available: "afplay", wantArgs: []string{"/bin/afplay", "/sound.wav"}},
		{name: "PulseAudio", goos: "linux", available: "paplay", wantArgs: []string{"/bin/paplay", "/sound.wav"}},
		{name: "ALSA fallback", goos: "linux", available: "aplay", wantArgs: []string{"/bin/aplay", "-q", "/sound.wav"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, err := playerCommand(context.Background(), test.goos, "/sound.wav", func(name string) (string, error) {
				if name == test.available {
					return "/bin/" + name, nil
				}
				return "", os.ErrNotExist
			})
			if err != nil {
				t.Fatalf("playerCommand() error = %v", err)
			}
			if !reflect.DeepEqual(command.Args, test.wantArgs) {
				t.Fatalf("command args = %q, want %q", command.Args, test.wantArgs)
			}
		})
	}
}

func TestPlayWritesAndRemovesTheEmbeddedSound(t *testing.T) {
	previousFind := findPlayer
	previousRun := runPlayer
	t.Cleanup(func() {
		findPlayer = previousFind
		runPlayer = previousRun
	})
	findPlayer = func(string) (string, error) { return "/bin/player", nil }
	var path string
	runPlayer = func(command *exec.Cmd) error {
		path = command.Args[len(command.Args)-1]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o600 {
			return errors.New("temporary sound is not private")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, doneWAV) {
			return errors.New("temporary file did not contain the done sound")
		}
		return nil
	}

	if err := Play(Done); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary sound still exists: %v", err)
	}
}
