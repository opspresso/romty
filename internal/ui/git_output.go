package ui

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

const maximumGitOutputBytes = 8 << 20

// gitCombinedOutput bounds retained output and stops the command on overflow.
// WaitDelay also bounds inherited pipes held open by a command's descendants.
func gitCombinedOutput(parent context.Context, path string, environment []string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	output := cappedGitOutput{limit: maximumGitOutputBytes, cancel: cancel}
	command := gitCommand(ctx, path, environment, arguments...)
	command.Stdout, command.Stderr = &output, &output
	command.WaitDelay = time.Second
	err := command.Run()
	if output.exceeded {
		return output.buffer.Bytes(), fmt.Errorf("Git output exceeds %d MiB limit", maximumGitOutputBytes>>20)
	}
	return output.buffer.Bytes(), err
}

type cappedGitOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (w *cappedGitOutput) Write(data []byte) (int, error) {
	count := min(len(data), w.limit-w.buffer.Len())
	_, _ = w.buffer.Write(data[:count])
	if count < len(data) && !w.exceeded {
		w.exceeded = true
		w.cancel()
	}
	return len(data), nil
}
