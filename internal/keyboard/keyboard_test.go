package keyboard

import "testing"

func TestStateFollowsKittyFlagsAsAStack(t *testing.T) {
	var state State
	if state.Flags() != 0 {
		t.Fatalf("Flags() = %d before any request, want 0", state.Flags())
	}
	state.Apply('>', []int{Disambiguate}, 'u')
	state.Apply('>', []int{Disambiguate | AlternateKeys}, 'u')
	if got := state.Flags(); got != Disambiguate|AlternateKeys {
		t.Fatalf("Flags() = %d after two pushes, want the last one", got)
	}
	state.Apply('<', nil, 'u')
	if got := state.Flags(); got != Disambiguate {
		t.Fatalf("Flags() = %d after a pop, want the first push back", got)
	}
	// A program that cannot know how deep the stack is pops more than exists.
	state.Apply('<', []int{9}, 'u')
	if got := state.Flags(); got != 0 {
		t.Fatalf("Flags() = %d after popping everything, want 0", got)
	}
}

func TestStateSetsFlagsInPlace(t *testing.T) {
	var state State
	state.Apply('=', []int{Disambiguate}, 'u')
	state.Apply('=', []int{AlternateKeys, 2}, 'u')
	if got := state.Flags(); got != Disambiguate|AlternateKeys {
		t.Fatalf("Flags() = %d after adding a bit, want both", got)
	}
	state.Apply('=', []int{Disambiguate, 3}, 'u')
	if got := state.Flags(); got != AlternateKeys {
		t.Fatalf("Flags() = %d after removing a bit, want the other left", got)
	}
	state.Apply('<', []int{-1}, 'u')
	if got := state.Flags(); got != 0 {
		t.Fatalf("Flags() = %d, want the flags set in place popped like a push", got)
	}
}

// romty forwards no key releases and no text as escape codes, so a program
// that asks for them must not be told it has them.
func TestStateKeepsOnlyTheFlagsItHonours(t *testing.T) {
	var state State
	state.Apply('>', []int{31}, 'u')
	if got := state.Flags(); got != Disambiguate|AlternateKeys {
		t.Fatalf("Flags() = %d, want only the supported flags", got)
	}
}

func TestStateKeepsEachScreensFlagsApart(t *testing.T) {
	var state State
	state.Apply('>', []int{Disambiguate}, 'u')
	state.SetAltScreen(true)
	if got := state.Flags(); got != 0 {
		t.Fatalf("Flags() = %d on the alternate screen, want its own empty stack", got)
	}
	state.Apply('>', []int{AlternateKeys}, 'u')
	state.SetAltScreen(false)
	if got := state.Flags(); got != Disambiguate {
		t.Fatalf("Flags() = %d back on the main screen, want its own push", got)
	}
}

func TestStateBoundsTheStack(t *testing.T) {
	var state State
	for range maxDepth * 3 {
		state.Apply('>', []int{Disambiguate}, 'u')
	}
	if depth := len(state.stacks[0]); depth != maxDepth {
		t.Fatalf("stack depth = %d, want at most %d", depth, maxDepth)
	}
}

func TestStateFollowsModifyOtherKeys(t *testing.T) {
	var state State
	if _, set := state.OtherKeys(); set {
		t.Fatal("OtherKeys() is set before the guest said anything")
	}
	state.Apply('>', []int{4, 0}, 'm')
	if level, set := state.OtherKeys(); !set || level != 0 {
		t.Fatalf("OtherKeys() = (%d, %v), want an explicit 0", level, set)
	}
	state.Apply('>', []int{4, 2}, 'm')
	if level, set := state.OtherKeys(); !set || level != 2 {
		t.Fatalf("OtherKeys() = (%d, %v), want 2", level, set)
	}
	state.Apply('>', []int{4}, 'm')
	if _, set := state.OtherKeys(); set {
		t.Fatal("OtherKeys() is still set after the guest reset it")
	}
	// Another XTMODKEYS resource is not this one.
	if state.Apply('>', []int{1, 2}, 'm') {
		t.Fatal("Apply took modifyCursorKeys for modifyOtherKeys")
	}
}

func TestStateIgnoresOtherSequences(t *testing.T) {
	var state State
	for _, probe := range []struct {
		private byte
		final   byte
	}{{'?', 'u'}, {0, 'u'}, {0, 'm'}, {'?', 'h'}} {
		if state.Apply(probe.private, []int{1}, probe.final) {
			t.Fatalf("Apply(%q, %q) reported a keyboard request", probe.private, probe.final)
		}
	}
}

func TestReportRestoresTheWholeState(t *testing.T) {
	var state State
	state.Apply('>', []int{Disambiguate}, 'u')
	state.Apply('>', []int{Disambiguate | AlternateKeys}, 'u')
	state.Apply('>', []int{4, 0}, 'm')
	state.SetAltScreen(true)
	state.Apply('>', []int{AlternateKeys}, 'u')

	report := state.Report()
	if report != "\x1b_romty:keyboard;1,5;4;0\x1b\\" {
		t.Fatalf("Report() = %q", report)
	}

	// The reader already holds pushes of its own, the way a client does
	// after a recording replayed them.
	var restored State
	restored.Apply('>', []int{Disambiguate}, 'u')
	restored.Apply('>', []int{Disambiguate}, 'u')
	restored.Apply('>', []int{Disambiguate}, 'u')
	restored.SetAltScreen(true)
	if !restored.Restore(report[2 : len(report)-2]) {
		t.Fatal("Restore refused a report")
	}
	if got := restored.Flags(); got != AlternateKeys {
		t.Fatalf("Flags() = %d on the alternate screen after Restore", got)
	}
	restored.SetAltScreen(false)
	restored.Apply('<', nil, 'u')
	if got := restored.Flags(); got != Disambiguate {
		t.Fatalf("Flags() = %d after one pop, want the reported stack and not the reader's own", got)
	}
	if level, set := restored.OtherKeys(); !set || level != 0 {
		t.Fatalf("OtherKeys() = (%d, %v) after Restore, want an explicit 0", level, set)
	}
}

func TestReportOfNothingClearsWhatTheReaderHeld(t *testing.T) {
	var empty State
	report := empty.Report()
	if report != "\x1b_romty:keyboard;;;\x1b\\" {
		t.Fatalf("Report() = %q", report)
	}
	var restored State
	restored.Apply('>', []int{Disambiguate}, 'u')
	restored.Apply('>', []int{4, 2}, 'm')
	if !restored.Restore(report[2 : len(report)-2]) {
		t.Fatal("Restore refused an empty report")
	}
	if _, set := restored.OtherKeys(); restored.Flags() != 0 || set {
		t.Fatal("Restore of an empty report left state behind")
	}
}

func TestRestoreRefusesWhatIsNotAReport(t *testing.T) {
	for _, payload := range []string{
		"", "Gf=100", "romty:keyboard;", "romty:keyboard;1;2", "romty:keyboard;x;;",
		"romty:keyboard;;;x", "romty:keyboard;1,1,1,1,1,1,1,1,1;;",
	} {
		state := State{}
		state.Apply('>', []int{Disambiguate}, 'u')
		if state.Restore(payload) {
			t.Fatalf("Restore(%q) accepted it", payload)
		}
		if state.Flags() != Disambiguate {
			t.Fatalf("Restore(%q) changed the state it refused", payload)
		}
	}
}
