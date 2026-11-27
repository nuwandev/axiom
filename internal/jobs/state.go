package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// The state file is an append-only log of JSON lines. It records what a job
// was (status, exit code, error, times) and which idempotency keys were used,
// so both survive a restart. It never records script output or parameter
// values, so nothing a script printed or was given reaches this file.
//
// It is a convenience, not a source of truth: the audit log is the durable
// record, and the service account that runs scripts can also write this file.
const (
	recordJob = "job"
	recordKey = "key"
)

type stateRecord struct {
	Type string `json:"type"`

	// Job records.
	ID          string    `json:"id,omitempty"`
	Action      string    `json:"action,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	Status      Status    `json:"status,omitempty"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`

	// Key records reuse Action and RequestedBy for the identity and action.
	Key        string    `json:"key,omitempty"`
	ParamsHash string    `json:"params_hash,omitempty"`
	JobID      string    `json:"job_id,omitempty"`
	Expires    time.Time `json:"expires"`
}

func (r stateRecord) validate() error {
	switch r.Type {
	case recordJob:
		if !ValidID(r.ID) {
			return fmt.Errorf("job record has an invalid id %q", r.ID)
		}
		switch r.Status {
		case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled:
		default:
			return fmt.Errorf("job %s has an unknown status %q", r.ID, r.Status)
		}
	case recordKey:
		if r.Key == "" || r.RequestedBy == "" || r.Action == "" || !ValidID(r.JobID) {
			return fmt.Errorf("incomplete idempotency key record")
		}
	default:
		return fmt.Errorf("unknown record type %q", r.Type)
	}
	return nil
}

// stateStore appends records durably and keeps the file bounded by compacting
// it. All access is serialised by mu; compaction needs nothing from the
// Manager because it works from the file's own contents.
type stateStore struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	lines      int
	compactAt  int
	maxHistory int
	now        func() time.Time
}

// compactionFactor is how many times maxHistory lines the log may reach before
// it is rewritten. Each job writes up to three lines, so this allows roughly
// three times maxHistory further jobs between compactions.
const compactionFactor = 10

// openStateStore reads the existing file, if any, and returns the records that
// are still relevant. The caller decides what to do with them and then calls
// rewrite, which also opens the file for appending.
//
// A final line that is cut short is what a crash in the middle of an append
// leaves behind, so it is dropped. Any other malformed line means the file
// was damaged or edited, and the agent refuses to start on it rather than
// quietly forget history.
func openStateStore(path string, maxHistory int, now func() time.Time) (*stateStore, []stateRecord, error) {
	s := &stateStore{path: path, maxHistory: maxHistory, compactAt: compactionFactor * maxHistory, now: now}

	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return s, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("checking job state file: %w", err)
	case !info.Mode().IsRegular():
		return nil, nil, fmt.Errorf("job state file %q is not a regular file", path)
	}

	recs, err := readStateFile(path)
	if err != nil {
		return nil, nil, err
	}
	return s, reduceState(recs, maxHistory, now()), nil
}

func readStateFile(path string) ([]stateRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading job state file: %w", err)
	}

	// Every complete record ends in a newline, so after the split the last
	// element is either empty (a clean file) or an unterminated tail.
	lines := bytes.Split(data, []byte{'\n'})
	tail := len(lines) - 1

	var recs []stateRecord
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r stateRecord
		err := json.Unmarshal(line, &r)
		if err == nil {
			err = r.validate()
		}
		if err != nil {
			if i == tail {
				// A crash in the middle of an append: nothing after it, and
				// it was never acknowledged, so dropping it loses nothing.
				break
			}
			return nil, fmt.Errorf("job state file %q line %d is damaged (%v); fix or remove the file", path, i+1, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// reduceState keeps the latest record per job and per idempotency key, the
// newest maxHistory finished jobs and every unfinished one, and the keys that
// have not expired. Jobs come back oldest first.
func reduceState(recs []stateRecord, maxHistory int, now time.Time) []stateRecord {
	jobs := map[string]stateRecord{}
	keys := map[[3]string]stateRecord{}
	for _, r := range recs {
		switch r.Type {
		case recordJob:
			jobs[r.ID] = r
		case recordKey:
			keys[[3]string{r.RequestedBy, r.Action, r.Key}] = r
		}
	}

	var live, done []stateRecord
	for _, r := range jobs {
		if r.Status.terminal() {
			done = append(done, r)
		} else {
			live = append(live, r)
		}
	}
	byCreated := func(s []stateRecord) {
		sort.Slice(s, func(i, j int) bool {
			if !s[i].CreatedAt.Equal(s[j].CreatedAt) {
				return s[i].CreatedAt.Before(s[j].CreatedAt)
			}
			return s[i].ID < s[j].ID
		})
	}
	byCreated(done)
	if len(done) > maxHistory {
		done = done[len(done)-maxHistory:]
	}
	byCreated(live)

	var liveKeys []stateRecord
	for _, r := range keys {
		if now.Before(r.Expires) {
			liveKeys = append(liveKeys, r)
		}
	}
	sort.Slice(liveKeys, func(i, j int) bool { return liveKeys[i].Expires.Before(liveKeys[j].Expires) })
	if len(liveKeys) > MaxIdempotencyKeys {
		liveKeys = liveKeys[len(liveKeys)-MaxIdempotencyKeys:]
	}

	finishedAndLive := append(done, live...)
	byCreated(finishedAndLive)
	return append(finishedAndLive, liveKeys...)
}

// rewrite replaces the file with exactly recs and opens it for appending. The
// new content is written and synced beside the old file and renamed over it,
// so a crash leaves either the complete old file or the complete new one.
func (s *stateStore) rewrite(recs []stateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewriteLocked(recs)
}

// rewriteLocked replaces the file. If that fails after the append handle was
// closed, it reopens the file that is still on disk, so a failed compaction
// cannot leave the store unable to record anything further.
func (s *stateStore) rewriteLocked(recs []stateRecord) error {
	err := s.replaceLocked(recs)
	if err != nil && s.file == nil {
		if f, openErr := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
			s.file = f
		}
	}
	return err
}

func (s *stateStore) replaceLocked(recs []stateRecord) error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return fmt.Errorf("closing job state file: %w", err)
		}
		s.file = nil
	}

	tmp := s.path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale job state temp file: %w", err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating job state temp file: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return fmt.Errorf("encoding job state record: %w", err)
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("writing job state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing job state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing job state temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replacing job state file: %w", err)
	}

	s.file, err = os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening job state file for append: %w", err)
	}
	s.lines = len(recs)
	return nil
}

// append writes one record and syncs it before returning, so a record that was
// acknowledged survives a crash. When the log has grown past compactAt it is
// compacted in place.
func (s *stateStore) append(r stateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding job state record: %w", err)
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing job state record: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("syncing job state record: %w", err)
	}
	s.lines++

	if s.lines < s.compactAt {
		return nil
	}
	recs, err := readStateFile(s.path)
	if err != nil {
		return err
	}
	return s.rewriteLocked(reduceState(recs, s.maxHistory, s.now()))
}

func (s *stateStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
