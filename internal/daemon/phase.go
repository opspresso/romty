package daemon

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

// Terminal-derived phases are a compatibility fallback for OpenCode. Codex
// and Claude Code require lifecycle events; redraws and silence cannot prove
// whether their work is active, awaiting a response, or complete.

// phaseHintBytes is how much of the end of the recording is read back. The
// recording holds megabytes; what an agent last drew is the final screen or
// two, and reading further back only finds prompts that were answered long ago.
const phaseHintBytes = 4 << 10

// An unhooked agent's redraws are an activity signal, not a lifecycle report.
// Let a few missed frames pass before treating silence as idle.
const agentActivityTimeout = 5 * time.Second

// phaseSignal is one phrase an agent draws and the phase it means.
type phaseSignal struct {
	text  string
	phase model.AgentPhase
}

// phaseSignals are matched against the lowercased end of the recording. They
// are phrases rather than words: a phrase an agent puts on screen deliberately
// is far less likely to appear in the output of the work it is doing.
var phaseSignals = []phaseSignal{
	// Interrupt hints. An agent shows these only while it is generating, so
	// seeing one is the clearest evidence that it has not stopped.
	{text: "esc to interrupt", phase: model.AgentPhaseWorking},
	{text: "esc to cancel", phase: model.AgentPhaseWorking},
	{text: "esc to stop", phase: model.AgentPhaseWorking},
	{text: "esc again to cancel", phase: model.AgentPhaseWorking},
	{text: "ctrl+c to stop", phase: model.AgentPhaseWorking},
	{text: "ctrl+c to interrupt", phase: model.AgentPhaseWorking},
	{text: "ctrl-c to interrupt", phase: model.AgentPhaseWorking},

	// Approval prompts. The agent stopped and named the choices it will accept.
	{text: "do you want to proceed", phase: model.AgentPhaseWaitingApproval},
	{text: "do you want to continue", phase: model.AgentPhaseWaitingApproval},
	{text: "waiting for approval", phase: model.AgentPhaseWaitingApproval},
	{text: "waiting for confirmation", phase: model.AgentPhaseWaitingApproval},
	{text: "allow this command", phase: model.AgentPhaseWaitingApproval},
	{text: "allow command?", phase: model.AgentPhaseWaitingApproval},
	{text: "allow editing file", phase: model.AgentPhaseWaitingApproval},
	{text: "allow creating file", phase: model.AgentPhaseWaitingApproval},
	{text: "allow execution", phase: model.AgentPhaseWaitingApproval},
	{text: "apply this change", phase: model.AgentPhaseWaitingApproval},
	{text: "allow all for this session", phase: model.AgentPhaseWaitingApproval},
	{text: "deny with feedback", phase: model.AgentPhaseWaitingApproval},
	{text: "reject & propose changes", phase: model.AgentPhaseWaitingApproval},
	{text: "yes, allow", phase: model.AgentPhaseWaitingApproval},
	{text: "esc or n or p", phase: model.AgentPhaseWaitingApproval},
	{text: "(y/n)", phase: model.AgentPhaseWaitingApproval},
	{text: "[y/n]", phase: model.AgentPhaseWaitingApproval},
	{text: "yes/no", phase: model.AgentPhaseWaitingApproval},

	// Input prompts. The agent stopped for an answer rather than a permission.
	{text: "press enter to continue", phase: model.AgentPhaseWaitingInput},
	{text: "press enter to confirm", phase: model.AgentPhaseWaitingInput},
	{text: "enter to submit answer", phase: model.AgentPhaseWaitingInput},
}

// titleSignals are matched against the whole window title. An agent sets its
// title deliberately, so a short phrase is enough where the same words in
// output would not be.
var titleSignals = []phaseSignal{
	{text: "action required", phase: model.AgentPhaseWaitingApproval},
	{text: "confirmation needed", phase: model.AgentPhaseWaitingApproval},
}

// inferAgentPhase reads back what an agent drew and reports the phase it
// suggests, or false when neither the output nor the title says anything.
//
// The newest signal in the output wins. An agent that drew an approval prompt
// and then went back to work printed its interrupt hint after that prompt, and
// taking the last phrase to appear is how a stream with no screen behind it
// recovers the order the screen would have shown.
func inferAgentPhase(output []byte, title string) (model.AgentPhase, bool) {
	if phase, ok := lastSignal(strings.ToLower(ansi.Strip(string(output))), phaseSignals); ok {
		return phase, true
	}
	// Only when the output is silent: a title is sticky, so it can outlive the
	// state it named, while output that says nothing cannot be stale.
	return lastSignal(strings.ToLower(title), titleSignals)
}

// TUI updates often replace only a spinner or elapsed time, leaving the
// interrupt hint on screen but outside the recording tail. Live output keeps
// work visible; silence expires the estimate so an old hint cannot keep an
// idle agent animating. Explicit input and approval prompts still take priority.
func inferAgentPhaseWithActivity(output []byte, title string, lastOutput, at time.Time) (model.AgentPhase, bool) {
	phase, found := inferAgentPhase(output, title)
	if found && phase != model.AgentPhaseWorking || lastOutput.IsZero() {
		return phase, found
	}
	if at.Sub(lastOutput) < agentActivityTimeout {
		return model.AgentPhaseWorking, true
	}
	return model.AgentPhaseIdle, true
}

// lastSignal is the phase of the signal that appears latest in text.
func lastSignal(text string, signals []phaseSignal) (model.AgentPhase, bool) {
	phase, at := model.AgentPhaseUnknown, -1
	for _, signal := range signals {
		if index := strings.LastIndex(text, signal.text); index > at {
			phase, at = signal.phase, index
		}
	}
	if at < 0 {
		return "", false
	}
	return phase, true
}
