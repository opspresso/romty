package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/service"
)

// serviceRunner runs a system tool; tests replace service.Run.
func serviceRunner(name string, arguments ...string) ([]byte, error) {
	return service.Run(name, arguments...)
}

// runService installs, removes, or reports the user service that starts the
// daemon at login and keeps it outside any one login's process tree.
func runService(arguments []string, output io.Writer, theme commandTheme) error {
	action := "status"
	if len(arguments) > 0 {
		action = arguments[0]
	}
	runtimePaths, err := paths.Resolve()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	definition := serviceDefinition{executable: executable, log: runtimePaths.Log}
	if os.Getenv("ROMTY_HOME") != "" {
		// Resolved, so the service finds the same directory whatever its
		// working directory is.
		definition.romtyHome = runtimePaths.Directory
	}
	platform, err := servicePlatformFor(runtime.GOOS, home)
	if err != nil {
		return err
	}
	switch action {
	case "install":
		// A daemon already running started outside the service and keeps
		// running outside it until it restarts.
		running := client.New(runtimePaths.Socket).Ping() == nil
		if err := platform.install(definition, output, theme); err != nil {
			return err
		}
		if running {
			return printField(output, theme, "next", "run romty stop and then romty to move the running daemon into the service")
		}
		return nil
	case "uninstall":
		return platform.uninstall(output, theme)
	case "status":
		return platform.status(output, theme)
	default:
		return fmt.Errorf("usage: romty service [install|uninstall|status]")
	}
}

// serviceDefinition is what the service runs.
type serviceDefinition struct {
	executable string
	log        string
	// romtyHome is carried into the service when set, so the daemon it
	// starts uses the same runtime directory as the romty that installed it.
	romtyHome string
}

type servicePlatform interface {
	install(serviceDefinition, io.Writer, commandTheme) error
	uninstall(io.Writer, commandTheme) error
	status(io.Writer, commandTheme) error
}

func servicePlatformFor(goos, home string) (servicePlatform, error) {
	path, err := service.File(goos, home)
	if err != nil {
		return nil, err
	}
	if goos == "darwin" {
		return launchdService{path: path}, nil
	}
	return systemdService{path: path}, nil
}

// launchdService is a LaunchAgent. It runs at login, not at boot: a macOS
// user session is what the shells need, with the user's keychain and agent.
type launchdService struct{ path string }

// launchdPlist is the job. ProcessType Interactive keeps launchd from
// throttling the daemon as a background job, which would slow every shell
// it runs. KeepAlive is off, so `romty stop` stops it.
func launchdPlist(definition serviceDefinition) string {
	var environment string
	if definition.romtyHome != "" {
		environment = "\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>ROMTY_HOME</key>\n\t\t<string>" +
			html.EscapeString(definition.romtyHome) + "</string>\n\t</dict>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + service.Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + html.EscapeString(definition.executable) + `</string>
		<string>daemon</string>
	</array>
` + environment + `	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>` + html.EscapeString(definition.log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + html.EscapeString(definition.log) + `</string>
</dict>
</plist>
`
}

func (s launchdService) domain() string {
	return service.LaunchdDomain()
}

func (s launchdService) install(definition serviceDefinition, output io.Writer, theme commandTheme) error {
	if err := writeServiceFile(s.path, launchdPlist(definition)); err != nil {
		return err
	}
	// A job already loaded keeps its old definition until it is booted out.
	_, _ = serviceRunner("launchctl", "bootout", s.domain()+"/"+service.Label)
	if result, err := serviceRunner("launchctl", "bootstrap", s.domain(), s.path); err != nil {
		return fmt.Errorf("load %s: %w: %s; it loads at the next login, or run launchctl bootstrap %s %s from a session logged in at the Mac",
			s.path, err, strings.TrimSpace(string(result)), s.domain(), s.path)
	}
	if err := printField(output, theme, "service", theme.good("installed")); err != nil {
		return err
	}
	return printField(output, theme, "file", s.path)
}

func (s launchdService) uninstall(output io.Writer, theme commandTheme) error {
	_, _ = serviceRunner("launchctl", "bootout", s.domain()+"/"+service.Label)
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", s.path, err)
	}
	return printField(output, theme, "service", theme.warning("not installed"))
}

func (s launchdService) status(output io.Writer, theme commandTheme) error {
	return printServiceFile(output, theme, s.path, "run romty service install to start the daemon at login")
}

// systemdService is a user unit. It runs in the user's systemd instance, so
// logind does not end it with the login that started it. Starting at boot,
// before anyone logs in, needs lingering, which the user enables.
type systemdService struct{ path string }

// systemdUnit is the unit. KillMode process sends SIGTERM to the daemon alone,
// which saves each tab's session and ends each tab's processes itself; a
// process a tab started in a session of its own, such as a tmux server, keeps
// running, as it does when the daemon runs outside systemd.
// StandardOutput=append needs systemd 240 or later.
func systemdUnit(definition serviceDefinition) string {
	environment := ""
	if definition.romtyHome != "" {
		environment = "Environment=" + strconv.Quote("ROMTY_HOME="+systemdEscape(definition.romtyHome)) + "\n"
	}
	return `[Unit]
Description=romty terminal workspace daemon

[Service]
Type=simple
ExecStart=` + strconv.Quote(strings.ReplaceAll(systemdEscape(definition.executable), "$", "$$")) + ` daemon
` + environment + `StandardOutput=append:` + systemdEscape(definition.log) + `
StandardError=append:` + systemdEscape(definition.log) + `
KillMode=process
TimeoutStopSec=20

[Install]
WantedBy=default.target
`
}

// systemdEscape keeps a % in a path from being read as a unit specifier.
func systemdEscape(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func (s systemdService) install(definition serviceDefinition, output io.Writer, theme commandTheme) error {
	if err := writeServiceFile(s.path, systemdUnit(definition)); err != nil {
		return err
	}
	for _, arguments := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", service.Unit}} {
		if result, err := serviceRunner("systemctl", arguments...); err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(result)))
		}
	}
	if err := printField(output, theme, "service", theme.good("installed")); err != nil {
		return err
	}
	if err := printField(output, theme, "file", s.path); err != nil {
		return err
	}
	return s.printLinger(output, theme)
}

func (s systemdService) uninstall(output io.Writer, theme commandTheme) error {
	_, _ = serviceRunner("systemctl", "--user", "disable", service.Unit)
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", s.path, err)
	}
	_, _ = serviceRunner("systemctl", "--user", "daemon-reload")
	return printField(output, theme, "service", theme.warning("not installed"))
}

func (s systemdService) status(output io.Writer, theme commandTheme) error {
	if err := printServiceFile(output, theme, s.path, "run romty service install to keep the daemon after logout"); err != nil {
		return err
	}
	return s.printLinger(output, theme)
}

// printLinger reports whether the user's systemd instance outlives their
// logins, which is what keeps the daemon running with nobody logged in.
func (s systemdService) printLinger(output io.Writer, theme commandTheme) error {
	result, err := serviceRunner("loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
	switch {
	case err != nil:
		return printField(output, theme, "linger", theme.warning("unknown"))
	case strings.TrimSpace(string(result)) == "yes":
		return printField(output, theme, "linger", theme.good("on"))
	default:
		if err := printField(output, theme, "linger", theme.warning("off")); err != nil {
			return err
		}
		return printField(output, theme, "next", "run loginctl enable-linger to keep the daemon with nobody logged in")
	}
}

func writeServiceFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func printServiceFile(output io.Writer, theme commandTheme, path, next string) error {
	if _, err := os.Stat(path); err != nil {
		if err := printField(output, theme, "service", theme.warning("not installed")); err != nil {
			return err
		}
		return printField(output, theme, "next", next)
	}
	if err := printField(output, theme, "service", theme.good("installed")); err != nil {
		return err
	}
	return printField(output, theme, "file", path)
}
