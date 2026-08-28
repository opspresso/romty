package daemon

import (
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/model"
)

func TestInferAgentPhaseReadsBackWhatTheAgentDrew(t *testing.T) {
	for _, probe := range []struct {
		name   string
		output string
		title  string
		phase  model.AgentPhase
		found  bool
	}{
		{name: "ordinary output says nothing", output: "make: nothing to be done\r\n"},
		{
			name:   "an interrupt hint means it is still generating",
			output: "reading files…\r\n  esc to interrupt\r\n",
			phase:  model.AgentPhaseWorking,
			found:  true,
		},
		{
			name:   "an approval prompt means it stopped to ask",
			output: "Bash(rm -rf build)\r\n  Do you want to proceed?\r\n",
			phase:  model.AgentPhaseWaitingApproval,
			found:  true,
		},
		{
			name:   "a yes-or-no prompt is an approval too",
			output: "Overwrite config.json? [y/N]",
			phase:  model.AgentPhaseWaitingApproval,
			found:  true,
		},
		{
			name:   "an answer prompt is waiting for input",
			output: "Which branch should I use?\r\n  Press Enter to confirm\r\n",
			phase:  model.AgentPhaseWaitingInput,
			found:  true,
		},
		{
			name:   "matching ignores case",
			output: "ESC TO INTERRUPT",
			phase:  model.AgentPhaseWorking,
			found:  true,
		},
		{
			name:   "colours around the phrase do not hide it",
			output: "\x1b[1m\x1b[38;5;208mDo you want to proceed?\x1b[0m",
			phase:  model.AgentPhaseWaitingApproval,
			found:  true,
		},
		{
			name:  "a title names a state the output did not",
			title: "codex — Action required",
			phase: model.AgentPhaseWaitingApproval,
			found: true,
		},
		{
			name:   "output that says something outranks a sticky title",
			output: "working…\r\n  esc to interrupt\r\n",
			title:  "codex — Action required",
			phase:  model.AgentPhaseWorking,
			found:  true,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			phase, found := inferAgentPhase([]byte(probe.output), probe.title)
			if found != probe.found || phase != probe.phase {
				t.Fatalf("inferAgentPhase() = (%q, %v), want (%q, %v)",
					phase, found, probe.phase, probe.found)
			}
		})
	}
}

// A stream has no screen behind it, so the order the phrases arrived in is the
// only record of which state came second.
func TestInferAgentPhaseTakesTheNewestSignal(t *testing.T) {
	answered := "Do you want to proceed?\r\n> yes\r\nediting…\r\n  esc to interrupt\r\n"
	if phase, found := inferAgentPhase([]byte(answered), ""); !found || phase != model.AgentPhaseWorking {
		t.Fatalf("answered prompt = (%q, %v), want it working again", phase, found)
	}
	asked := "editing…\r\n  esc to interrupt\r\nBash(git push)\r\n  Do you want to proceed?\r\n"
	if phase, found := inferAgentPhase([]byte(asked), ""); !found || phase != model.AgentPhaseWaitingApproval {
		t.Fatalf("new prompt = (%q, %v), want it waiting", phase, found)
	}
}

// The recording holds megabytes; a prompt answered long ago must not keep
// reporting itself.
func TestPhaseHintReadsOnlyTheEndOfTheRecording(t *testing.T) {
	value := &recording{}
	value.append([]byte("Do you want to proceed?\r\n"))
	value.append([]byte(strings.Repeat("build output\r\n", phaseHintBytes)))
	if phase, found := inferAgentPhase(value.tail(phaseHintBytes), ""); found {
		t.Fatalf("phase = %q, want the old prompt scrolled out of reach", phase)
	}
}
