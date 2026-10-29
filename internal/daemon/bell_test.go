package daemon

import "testing"

func TestBellScannerIgnoresStringTerminators(t *testing.T) {
	for _, probe := range []struct {
		name   string
		chunks []string
		want   bool
	}{
		{"plain bell", []string{"done\a"}, true},
		{"title set at a prompt", []string{"\x1b]0;user@host\a$ "}, false},
		{"title split across reads", []string{"\x1b]0;us", "er\a$ "}, false},
		{"title ended by ST, then a bell", []string{"\x1b]2;x\x1b\\", "\a"}, true},
		{"DCS with BEL inside", []string{"\x1bPq\a"}, false},
		{"CSI then bell", []string{"\x1b[31m\a"}, true},
	} {
		var scanner bellScanner
		rang := false
		for _, chunk := range probe.chunks {
			rang = scanner.scan([]byte(chunk)) || rang
		}
		if rang != probe.want {
			t.Errorf("%s: rang = %v, want %v", probe.name, rang, probe.want)
		}
	}
}
