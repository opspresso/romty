package gitrepo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"
)

const maxOutputBytes = 8 << 20

// combinedOutput bounds retained output and stops the command on overflow.
// WaitDelay also bounds inherited pipes held open by a command's descendants.
func combinedOutput(parent context.Context, path string, environment []string, arguments ...string) ([]byte, error) {
	return capturedOutput(parent, path, environment, true, arguments...)
}

// stdout captures machine-readable Git output without mixing in diagnostics.
func stdout(parent context.Context, path string, environment []string, arguments ...string) ([]byte, error) {
	return capturedOutput(parent, path, environment, false, arguments...)
}

func capturedOutput(parent context.Context, path string, environment []string, combined bool, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	output := cappedOutput{limit: maxOutputBytes, cancel: cancel}
	command := command(ctx, path, environment, arguments...)
	command.Stdout = &output
	if combined {
		command.Stderr = &output
	} else {
		command.Stderr = io.Discard
	}
	command.WaitDelay = time.Second
	err := command.Run()
	if output.exceeded {
		return output.buffer.Bytes(), fmt.Errorf("Git output exceeds %d MiB limit", maxOutputBytes>>20)
	}
	return output.buffer.Bytes(), err
}

type cappedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (w *cappedOutput) Write(data []byte) (int, error) {
	count := min(len(data), w.limit-w.buffer.Len())
	_, _ = w.buffer.Write(data[:count])
	if count < len(data) && !w.exceeded {
		w.exceeded = true
		w.cancel()
	}
	return len(data), nil
}
