package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestServiceDefinitionsRunTheDaemonIntoItsLog(t *testing.T) {
	definition := serviceDefinition{executable: "/opt/romty & co/romty", log: "/h/.config/romty/daemon.log", romtyHome: "/h/r"}
	plist := launchdPlist(definition)
	for _, want := range []string{"<string>/opt/romty &amp; co/romty</string>", "<string>daemon</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<false/>", "<string>Interactive</string>",
		"<key>ROMTY_HOME</key>", "<string>/h/.config/romty/daemon.log</string>"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist lacks %q:\n%s", want, plist)
		}
	}
	unit := systemdUnit(definition)
	for _, want := range []string{`ExecStart="/opt/romty & co/romty" daemon`, `Environment="ROMTY_HOME=/h/r"`,
		"StandardError=append:/h/.config/romty/daemon.log", "KillMode=mixed", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
}

// install writes the file and loads it; uninstall removes it; status reports
// it. The system tools are recorded, not run.
func TestServiceInstallUninstallAndStatus(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			var ran []string
			testutil.Set(t, &serviceRunner, func(name string, arguments ...string) ([]byte, error) {
				ran = append(ran, name+" "+strings.Join(arguments, " "))
				return []byte("no\n"), nil
			})
			platform, err := servicePlatformFor(goos, home)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := platform.install(serviceDefinition{executable: "/bin/romty", log: "/l"}, &output, commandTheme{}); err != nil {
				t.Fatalf("install error = %v", err)
			}
			files, _ := filepath.Glob(filepath.Join(home, "*", "*", "*"))
			files2, _ := filepath.Glob(filepath.Join(home, "*", "*", "*", "*"))
			if len(files)+len(files2) == 0 {
				t.Fatal("install wrote no service file")
			}
			loads := strings.Join(ran, "\n")
			if goos == "darwin" && !strings.Contains(loads, "launchctl bootstrap gui/") ||
				goos == "linux" && !strings.Contains(loads, "systemctl --user enable romty.service") {
				t.Fatalf("install ran:\n%s", loads)
			}
			if goos == "linux" && !strings.Contains(output.String(), "next:       run loginctl enable-linger") {
				t.Fatalf("linux install does not explain lingering:\n%s", output.String())
			}
			output.Reset()
			if err := platform.uninstall(&output, commandTheme{}); err != nil {
				t.Fatalf("uninstall error = %v", err)
			}
			output.Reset()
			if err := platform.status(&output, commandTheme{}); err != nil || !strings.Contains(output.String(), "not installed") {
				t.Fatalf("status after uninstall = (%q, %v)", output.String(), err)
			}
		})
	}
	if _, err := servicePlatformFor("windows", t.TempDir()); err == nil {
		t.Fatal("an unsupported system was accepted")
	}
}
