package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	observationTTL        = 60 * time.Second
	observationLockWait   = 20 * time.Millisecond
	maxObservationBytes   = 64 * 1024
	maxObservationEntries = 128
	observedExecute       = "capy_execute"
	observedFetch         = "capy_fetch_and_index"
)

var errObservationLockTimeout = errors.New("tool observation lock wait exceeded")

type toolObservation struct {
	Session string           `json:"session"`
	Agent   string           `json:"agent"`
	Tools   map[string]int64 `json:"tools"` // successful observation times, Unix milliseconds
}

type observationState struct {
	Entries []toolObservation `json:"entries"`
}

type observationStore struct {
	dir string
	now func() time.Time
}

func newObservationStore(projectDir string) observationStore {
	store := observationStore{now: time.Now}
	if projectDir != "" {
		store.dir = filepath.Join(projectDir, ".capy")
	}
	return store
}

func (s observationStore) record(session, agent, tool string) error {
	if session == "" || agent == "" || !isObservedTool(tool) {
		return nil
	}
	session, agent = sessionIDComponent(session), sessionIDComponent(agent)
	return s.update(true, func(state *observationState, now int64) bool {
		state.expire(now)
		for i := range state.Entries {
			entry := &state.Entries[i]
			if entry.Session == session && entry.Agent == agent {
				entry.Tools[tool] = now
				return true
			}
		}
		state.Entries = append(state.Entries, toolObservation{
			Session: session, Agent: agent, Tools: map[string]int64{tool: now},
		})
		return true
	})
}

// consume commits removal of ALL alternatives for the child before returning a
// suitable tool. An unsuitable observation remains available for another route.
func (s observationStore) consume(session, agent string, allowFetch bool) (string, error) {
	if session == "" || agent == "" {
		return "", nil
	}
	session, agent = sessionIDComponent(session), sessionIDComponent(agent)
	tool := ""
	err := s.update(false, func(state *observationState, now int64) bool {
		changed := state.expire(now)
		for i, entry := range state.Entries {
			if entry.Session != session || entry.Agent != agent {
				continue
			}
			if allowFetch && entry.Tools[observedFetch] != 0 {
				tool = observedFetch
			} else if entry.Tools[observedExecute] != 0 {
				tool = observedExecute
			}
			if tool != "" {
				state.Entries = append(state.Entries[:i], state.Entries[i+1:]...)
				return true
			}
		}
		return changed
	})
	if err != nil {
		return "", err // never publish evidence whose consumption failed
	}
	return tool, nil
}

func (s observationStore) removeSession(session string) error {
	if session == "" {
		return nil
	}
	session = sessionIDComponent(session)
	return s.update(false, func(state *observationState, _ int64) bool {
		kept := state.Entries[:0]
		for _, entry := range state.Entries {
			if entry.Session != session {
				kept = append(kept, entry)
			}
		}
		changed := len(kept) != len(state.Entries)
		state.Entries = kept
		return changed
	})
}

func (s observationStore) update(create bool, mutate func(*observationState, int64) bool) error {
	if s.dir == "" {
		return nil
	}
	path := filepath.Join(s.dir, "tool-observations.json")
	if !create {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil // native fallback/SessionEnd must not create unused state
		} else if err != nil {
			return err
		}
	} else if err := os.Mkdir(s.dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("tool observation directory is not a regular directory")
	}
	lock, err := lockObservations(filepath.Join(s.dir, "tool-observations.lock"))
	if err != nil {
		return err
	}
	defer lock.Close() // closing releases flock; NEVER unlink/replace the lock file
	now := s.now().UnixMilli()
	if now <= 0 {
		return fmt.Errorf("invalid tool observation clock")
	}
	state, err := readObservations(path, now)
	if err != nil {
		return err
	}
	if !mutate(&state, now) {
		return nil
	}
	data, err := state.boundedJSON()
	if err != nil {
		return err
	}
	return writeObservations(path, data)
}

func lockObservations(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = fmt.Errorf("tool observation lock is not a regular file")
		}
		return nil, err
	}
	deadline := time.Now().Add(observationLockWait)
	for time.Now().Before(deadline) {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(min(time.Millisecond, remaining))
		}
	}
	f.Close()
	return nil, errObservationLockTimeout
}

func readObservations(path string, now int64) (observationState, error) {
	state := observationState{Entries: []toolObservation{}}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxObservationBytes {
		return state, fmt.Errorf("tool observations must be a regular file of at most %d bytes", maxObservationBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxObservationBytes+1))
	if err != nil {
		return state, err
	}
	if len(data) > maxObservationBytes {
		return state, fmt.Errorf("tool observations exceeded byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	state = observationState{}
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode tool observations: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return state, fmt.Errorf("trailing tool observation data")
	}
	if state.Entries == nil || len(state.Entries) > maxObservationEntries {
		return state, fmt.Errorf("invalid tool observation entries")
	}
	seen := make(map[[2]string]bool, len(state.Entries))
	for _, entry := range state.Entries {
		pair := [2]string{entry.Session, entry.Agent}
		if entry.Session == "" || entry.Agent == "" || seen[pair] ||
			sessionIDComponent(entry.Session) != entry.Session || sessionIDComponent(entry.Agent) != entry.Agent ||
			len(entry.Tools) == 0 || len(entry.Tools) > 2 {
			return state, fmt.Errorf("invalid tool observation identity or tools")
		}
		seen[pair] = true
		for tool, timestamp := range entry.Tools {
			if !isObservedTool(tool) || timestamp <= 0 || timestamp > now {
				return state, fmt.Errorf("invalid tool observation timestamp or tool")
			}
		}
	}
	return state, nil
}

func (state *observationState) expire(now int64) bool {
	changed := false
	kept := state.Entries[:0]
	for _, entry := range state.Entries {
		for tool, timestamp := range entry.Tools {
			if now-timestamp >= observationTTL.Milliseconds() {
				delete(entry.Tools, tool)
				changed = true
			}
		}
		if len(entry.Tools) != 0 {
			kept = append(kept, entry)
		}
	}
	state.Entries = kept
	return changed
}

func (state *observationState) boundedJSON() ([]byte, error) {
	for {
		data, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		if len(state.Entries) <= maxObservationEntries && len(data) <= maxObservationBytes {
			return data, nil
		}
		oldest := 0
		for i, entry := range state.Entries {
			latest := max(entry.Tools[observedExecute], entry.Tools[observedFetch])
			candidate := state.Entries[oldest]
			if latest < max(candidate.Tools[observedExecute], candidate.Tools[observedFetch]) {
				oldest = i
			}
		}
		state.Entries = append(state.Entries[:oldest], state.Entries[oldest+1:]...)
	}
}

func writeObservations(path string, data []byte) error {
	// The stable lock serializes writers, so one staging name suffices. Reusing
	// it also bounds leftovers if a hook is killed before its rename/cleanup.
	staging := path + ".tmp"
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(staging) // a failed write never replaces the prior data file
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(staging, path)
}

func isObservedTool(tool string) bool {
	return tool == observedExecute || tool == observedFetch
}
