package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// SocketPathLimit is how much room the kernel gives a unix socket path: the
// sun_path field of sockaddr_un, 104 bytes on macOS and 108 on Linux. Deriving
// it from the struct is what keeps both right without a build tag each. One
// byte of it belongs to the terminator, so a path has to be shorter than this
// — a socket at exactly the limit is refused with `invalid argument`.
//
// It is exported because the tests build romty homes of their own and have to
// keep them under the same ceiling.
const SocketPathLimit = len(syscall.RawSockaddrUnix{}.Path)

type Paths struct {
	Directory string
	Socket    string
	State     string
	Config    string
	Log       string
}

func Resolve() (Paths, error) {
	directory := os.Getenv("ROMTY_HOME")
	if directory == "" {
		configDirectory, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		directory = filepath.Join(configDirectory, "romty")
	}

	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve romty directory: %w", err)
	}
	socket := filepath.Join(absolute, "daemon.sock")
	// Checked here, where the path is built, because everything romty does
	// goes through this socket: the TUI, `romty stop` and the daemon all fail
	// on a path the kernel will not take. What they failed with was `bind:
	// invalid argument` or `connect: invalid argument`, which says nothing
	// about a length, names no limit, and offers nothing to change — and the
	// only way to reach it is ROMTY_HOME, which the error can name.
	if len(socket) >= SocketPathLimit {
		return Paths{}, fmt.Errorf(
			"romty directory is too deep: its daemon socket %s is %d bytes and a unix socket path must be under %d; set ROMTY_HOME to a shorter directory",
			socket, len(socket), SocketPathLimit)
	}
	return Paths{
		Directory: absolute,
		Socket:    socket,
		State:     filepath.Join(absolute, "state.json"),
		Config:    filepath.Join(absolute, "config.json"),
		Log:       filepath.Join(absolute, "daemon.log"),
	}, nil
}

func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Directory, 0o700); err != nil {
		return fmt.Errorf("create romty directory: %w", err)
	}
	return nil
}
