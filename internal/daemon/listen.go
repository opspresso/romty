// Owning the daemon socket: the lock that makes one daemon the only one, and
// the private socket it listens on.
package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// lockSuffix names the lock beside the socket it guards. It is derived rather
// than configured because the two only mean anything together: a lock pointed
// at another path guards nothing.
const lockSuffix = ".lock"

// lockDaemon takes the exclusive lock that says which daemon owns the socket.
// The kernel releases it when the process ends however it ends, so a daemon
// that was killed outright leaves nothing to clean up — which a PID file, the
// other way to answer this, would.
func lockDaemon(path string) (*os.File, error) {
	fd, err := syscall.Open(path,
		syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("inspect daemon lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("daemon lock must be a regular file owned only by the current user")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("set daemon lock permissions: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock daemon: %w", err)
	}
	return file, nil
}

func prepareSocket(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect unix socket: %w", err)
	}

	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale unix socket: %w", err)
	}
	return nil
}

// listenPrivately binds the unix socket so that it is never, even for an
// instant, reachable by another user. The socket's own directory is what makes
// that true: Serve narrows it to 0700 before this runs, so no other user can
// reach the name at all, whatever mode the bind leaves on it. Narrowing the
// umask around the bind instead would say the same thing about the socket and
// something else about every other file the process happens to create at that
// moment — the umask is process-wide, and a daemon shares its process with a
// TUI in development and with the whole suite under test.
func listenPrivately(path string) (net.Listener, error) {
	listener, err := net.Listen("unix", path)
	if err != nil {
		// The bind is what decides who owns the socket, not the probe in
		// prepareSocket: two daemons starting at once can both find nothing to
		// dial and race to create it. The one that loses is not broken, and
		// reporting that as a bind failure sent it to daemon.log as an error
		// and exited non-zero for what is the ordinary outcome of a race.
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	return listener, nil
}
