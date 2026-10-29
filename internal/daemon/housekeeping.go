package daemon

import (
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/logfile"
	"github.com/opspresso/romty/internal/model"
)

// housekeepingInterval is how often the daemon checks the work below. It
// is a variable so tests need not wait it out.
var housekeepingInterval = 30 * time.Second

// recordingBudget bounds what all recordings hold together, and
// idleRecordingBytes is what a quiet tab keeps once the budget is reached.
// Each tab may hold maxHistoryBytes on its own, so fifty busy tabs would
// otherwise hold 400 MiB.
var (
	recordingBudget    = 256 << 20
	idleRecordingBytes = 1 << 20
)

// SetLogFile names daemon.log, which the daemon rotates while it runs. The
// daemon's standard output and error must be that file; the check is made
// before each rotation.
func (s *Server) SetLogFile(path string) {
	s.logPath = path
}

// SetConfigFile names config.json, where allow_sleep turns off keeping the
// machine awake while an agent works.
func (s *Server) SetConfigFile(path string) {
	s.configPath = path
}

// housekeep runs the periodic work until the daemon stops.
func (s *Server) housekeep() {
	ticker := time.NewTicker(housekeepingInterval)
	defer ticker.Stop()
	for {
		s.housekeepOnce()
		select {
		case <-s.stop:
			s.awake.release()
			return
		case <-ticker.C:
		}
	}
}

// checkpointInterval is how often the agent tabs' sessions are checkpointed.
var checkpointInterval = 5 * time.Minute

func (s *Server) housekeepOnce() {
	s.enforceRecordingBudget()
	if time.Since(s.lastCheckpoint) >= checkpointInterval {
		s.lastCheckpoint = time.Now()
		s.checkpointAgents()
	}
	if s.logPath != "" {
		if err := rotateDaemonLog(s.logPath); err != nil {
			s.logger.Printf("rotate daemon log: %v", err)
		}
	}
	s.updateAwake()
}

// checkpointAgents saves the session of every tab running an agent, the way
// a stop does, into the checkpoint store. Only agent tabs: they are the ones
// with a session to resume, and a plain shell's output is not worth writing
// every few minutes.
//
// A tab is written again only when it printed since its last checkpoint or
// its agent session changed, so an idle agent costs no disk writes. A tab
// that closed or ended while its checkpoint was being written has the file
// removed again, so a closed session never comes back after a crash.
func (s *Server) checkpointAgents() {
	s.mu.Lock()
	saves := make([]resumeSave, 0)
	now := time.Now()
	if s.checkpointed == nil {
		s.checkpointed = make(map[string]checkpointMark)
	}
	for _, tab := range s.value.Tabs {
		value, running := s.sessions[tab.ID]
		if !running || s.agentStatuses[tab.ID].Agent == "" {
			continue
		}
		save := s.resumeSaveLocked(tab, value, now)
		last, seen := s.checkpointed[tab.ID]
		if seen && !value.lastOutput().After(last.at) && last.agent == save.meta.Agent &&
			last.session == save.meta.AgentSessionID {
			continue
		}
		saves = append(saves, save)
	}
	s.mu.Unlock()
	for _, save := range saves {
		if err := s.checkpoint.save(save.id, save.meta, save.recording()); err != nil {
			s.logger.Printf("checkpoint tab %s: %v", save.id, err)
			continue
		}
		s.mu.Lock()
		_, running := s.sessions[save.id]
		if running {
			s.checkpointed[save.id] = checkpointMark{at: now, agent: save.meta.Agent, session: save.meta.AgentSessionID}
		}
		s.mu.Unlock()
		if !running {
			s.checkpoint.remove(save.id)
		}
	}
}

// checkpointMark is what a tab's last checkpoint saved, so an unchanged tab
// is not written again.
type checkpointMark struct {
	at      time.Time
	agent   model.Agent
	session string
}

// enforceRecordingBudget trims the recordings of the quietest tabs to
// idleRecordingBytes until all of them fit in recordingBudget. A trimmed tab
// replays less output when reattached; it keeps everything it prints next.
func (s *Server) enforceRecordingBudget() {
	s.mu.Lock()
	sessions := slices.Collect(func(yield func(*session) bool) {
		for _, value := range s.sessions {
			if !yield(value) {
				return
			}
		}
	})
	s.mu.Unlock()
	total := 0
	for _, value := range sessions {
		total += value.recordingCapacity()
	}
	if total <= recordingBudget {
		return
	}
	slices.SortFunc(sessions, func(a, b *session) int { return a.lastOutput().Compare(b.lastOutput()) })
	for _, value := range sessions {
		if total <= recordingBudget {
			break
		}
		total -= value.trimRecording(idleRecordingBytes)
	}
}

// rotateDaemonLog archives a full daemon.log and points the daemon's standard
// output and error at the new one. It leaves a log alone that is not the
// daemon's standard error, such as when the daemon runs in a terminal.
func rotateDaemonLog(path string) error {
	if !sameFile(path, os.Stderr) {
		return nil
	}
	file, err := logfile.RotateIfFull(path)
	if file == nil || err != nil {
		return err
	}
	defer file.Close()
	for _, target := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		if err := redirect(int(file.Fd()), target); err != nil {
			return err
		}
	}
	return nil
}

func sameFile(path string, file *os.File) bool {
	pathInfo, err := os.Stat(path)
	if err != nil {
		return false
	}
	fileInfo, err := file.Stat()
	return err == nil && os.SameFile(pathInfo, fileInfo)
}

// awakeHold keeps the machine from idle sleep while an agent works, by
// running the system's own tool: caffeinate on macOS, systemd-inhibit on
// Linux. The tool ends with the daemon, so a hold never outlives it.
type awakeHold struct {
	mu      sync.Mutex
	command *exec.Cmd
	// missing records that the tool is not installed, logged once.
	missing bool
}

// updateAwake holds the machine awake while any agent is busy, unless
// config.json sets allow_sleep.
func (s *Server) updateAwake() {
	s.updateAwakeFrom(s.agentStatusesSnapshot())
}

// updateAwakeFrom holds or releases from statuses already scanned. Every
// fresh status scan calls it, so the hold follows an agent within a TUI poll
// rather than within a housekeeping round.
func (s *Server) updateAwakeFrom(statuses map[string]model.AgentStatus) {
	busy := false
	if !s.sleepAllowed() {
		for _, status := range statuses {
			if status.Phase.Busy() {
				busy = true
				break
			}
		}
	}
	if busy {
		s.awake.hold(s)
	} else {
		s.awake.release()
	}
}

func (s *Server) sleepAllowed() bool {
	return s.settings().AllowSleep
}

func (h *awakeHold) hold(s *Server) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.command != nil || h.missing {
		return
	}
	command := awakeCommand(os.Getpid())
	// exec.Command records a tool it cannot find in Err.
	if err := command.Start(); err != nil {
		h.missing = true
		s.logger.Printf("keep awake: %v", err)
		return
	}
	h.command = command
	started := time.Now()
	go func() {
		err := command.Wait()
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.command != command {
			// Released on purpose.
			return
		}
		// The tool ended by itself, so nothing holds the machine awake. One
		// that ends at once cannot hold here, for example without logind
		// or D-Bus, and is not started again.
		h.command = nil
		if time.Since(started) < housekeepingInterval {
			h.missing = true
		}
		s.logger.Printf("keep awake: %s ended: %v", command.Path, err)
	}()
}

func (h *awakeHold) release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.command == nil {
		return
	}
	_ = h.command.Process.Signal(syscall.SIGTERM)
	h.command = nil
}

// holding reports whether the hold is in place.
func (h *awakeHold) holding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.command != nil
}
