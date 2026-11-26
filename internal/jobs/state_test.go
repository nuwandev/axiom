package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var stateEpoch = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func jobRec(id string, status Status, minute int) stateRecord {
	return stateRecord{Type: recordJob, ID: id, Action: "a", RequestedBy: "ci", Status: status, CreatedAt: stateEpoch.Add(time.Duration(minute) * time.Minute)}
}

func keyRec(key, jobID string, expires time.Time) stateRecord {
	return stateRecord{Type: recordKey, RequestedBy: "ci", Action: "a", Key: key, ParamsHash: "00", JobID: jobID, Expires: expires}
}

func ids(recs []stateRecord, typ string) []string {
	var out []string
	for _, r := range recs {
		if r.Type != typ {
			continue
		}
		if typ == recordJob {
			out = append(out, r.ID)
		} else {
			out = append(out, r.Key)
		}
	}
	return out
}

// Fixed, valid job IDs: ValidID accepts 26 Crockford base32 characters.
const (
	idA = "01AAAAAAAAAAAAAAAAAAAAAAAA"
	idB = "01BBBBBBBBBBBBBBBBBBBBBBBB"
	idC = "01CCCCCCCCCCCCCCCCCCCCCCCC"
	idD = "01DDDDDDDDDDDDDDDDDDDDDDDD"
)

func TestReduceState_LatestRecordWinsAndOrderIsOldestFirst(t *testing.T) {
	recs := []stateRecord{
		jobRec(idB, StatusQueued, 2),
		jobRec(idA, StatusQueued, 1),
		jobRec(idB, StatusRunning, 2),
		jobRec(idB, StatusSucceeded, 2),
	}
	got := reduceState(recs, 10, stateEpoch)
	if want := []string{idA, idB}; strings.Join(ids(got, recordJob), ",") != strings.Join(want, ",") {
		t.Fatalf("jobs = %v, want %v", ids(got, recordJob), want)
	}
	for _, r := range got {
		if r.ID == idB && r.Status != StatusSucceeded {
			t.Fatalf("job B status = %s, want the latest record (succeeded)", r.Status)
		}
	}
}

func TestReduceState_KeepsNewestFinishedJobsAndEveryLiveOne(t *testing.T) {
	recs := []stateRecord{
		jobRec(idA, StatusSucceeded, 1),
		jobRec(idB, StatusSucceeded, 2),
		jobRec(idC, StatusSucceeded, 3),
		jobRec(idD, StatusRunning, 0), // oldest, but still live: never dropped
	}
	got := ids(reduceState(recs, 2, stateEpoch), recordJob)
	if want := []string{idD, idB, idC}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
}

func TestReduceState_DropsExpiredKeysAndKeepsTheLatestPerKey(t *testing.T) {
	recs := []stateRecord{
		keyRec("old", idA, stateEpoch.Add(-time.Minute)),
		keyRec("live", idA, stateEpoch.Add(time.Hour)),
		keyRec("live", idB, stateEpoch.Add(2*time.Hour)),
	}
	got := reduceState(recs, 10, stateEpoch)
	if keys := ids(got, recordKey); len(keys) != 1 || keys[0] != "live" {
		t.Fatalf("keys = %v, want [live]", keys)
	}
	if got[0].JobID != idB {
		t.Fatalf("kept job %s, want the later record's %s", got[0].JobID, idB)
	}
}

func writeState(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jobs.state")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodLine(id string) string {
	return `{"type":"job","id":"` + id + `","action":"a","requested_by":"ci","status":"succeeded","created_at":"2026-11-01T12:00:00Z","started_at":"0001-01-01T00:00:00Z","finished_at":"0001-01-01T00:00:00Z","expires":"0001-01-01T00:00:00Z"}`
}

func TestReadStateFile_TruncatedLastLineIsDropped(t *testing.T) {
	p := writeState(t, goodLine(idA)+"\n"+`{"type":"job","id":"`+idB+`","sta`)
	recs, err := readStateFile(p)
	if err != nil || len(recs) != 1 || recs[0].ID != idA {
		t.Fatalf("recs = %+v, err = %v, want only job A", recs, err)
	}
}

func TestReadStateFile_DamageBeforeTheEndRefusesToLoad(t *testing.T) {
	p := writeState(t, goodLine(idA)+"\nnot json\n"+goodLine(idB)+"\n")
	_, err := readStateFile(p)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want one naming line 2", err)
	}
}

func TestReadStateFile_AnInvalidRecordIsDamageNotSilentlyKept(t *testing.T) {
	for name, line := range map[string]string{
		"bad id":     `{"type":"job","id":"nope","status":"succeeded"}`,
		"bad status": `{"type":"job","id":"` + idA + `","status":"exploded"}`,
		"unknown":    `{"type":"mystery"}`,
		"incomplete": `{"type":"key","key":"k"}`,
	} {
		p := writeState(t, line+"\n"+goodLine(idB)+"\n")
		if _, err := readStateFile(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOpenStateStore_RefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := openStateStore(dir, 10, time.Now); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file error", err)
	}
}

func TestStateStore_AppendSurvivesReopenAndCompactionBoundsTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jobs.state")
	s, _, err := openStateStore(p, 3, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.rewrite(nil); err != nil {
		t.Fatal(err)
	}
	defer s.close()

	// 3 finished jobs are retained; compaction triggers at 10*3 = 30 lines.
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("01%024d", i)
		if err := s.append(jobRec(id, StatusSucceeded, i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	recs, err := readStateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) > 30 {
		t.Fatalf("file holds %d records, compaction should keep it under 30", len(recs))
	}
	if kept := reduceState(recs, 3, time.Now()); len(kept) != 3 {
		t.Fatalf("a reload would keep %d jobs, want 3", len(kept))
	}
}

func mustJSON(t *testing.T, r stateRecord) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
