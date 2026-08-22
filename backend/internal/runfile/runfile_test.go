package runfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "running.json")
	want := Info{
		PID: 4242, Port: 3001,
		StartedAt: time.Now().UTC().Truncate(time.Second),
		AppRunID:  "apprun-1",
	}

	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil for an existing file")
		return
	}
	if got.PID != want.PID || got.Port != want.Port || got.AppRunID != want.AppRunID || !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("round trip mismatch: got %+v, want %+v", *got, want)
	}
}

// TestWriteOverwritesExisting is the cross-platform overwrite check: a stale
// running.json from a crashed predecessor must be replaced cleanly. POSIX
// rename(2) handles this natively; Windows needs MoveFileEx with
// MOVEFILE_REPLACE_EXISTING — atomicReplace gives us both.
func TestWriteReadRoundTripOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")

	// app-owned daemon: Owner round-trips as "app".
	want := Info{PID: 1, Port: 3001, Owner: "app"}
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil for an existing file")
		return
	}
	if got.Owner != "app" {
		t.Errorf("Owner round trip: got %q, want %q", got.Owner, "app")
	}

	// headless daemon: Owner is empty (omitempty), round-trips as "".
	headless := Info{PID: 2, Port: 3002}
	if err := Write(path, headless); err != nil {
		t.Fatalf("Write headless: %v", err)
	}
	got, err = Read(path)
	if err != nil {
		t.Fatalf("Read headless: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil for headless file")
		return
	}
	if got.Owner != "" {
		t.Errorf("headless Owner round trip: got %q, want %q", got.Owner, "")
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")

	if err := Write(path, Info{PID: 1, Port: 3001}); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := Write(path, Info{PID: 2, Port: 3002}); err != nil {
		t.Fatalf("second Write (overwrite): %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil || got.PID != 2 || got.Port != 3002 {
		t.Errorf("after overwrite: got %+v, want PID=2 Port=3002", got)
	}
}

func TestReadMissingIsNotError(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Read missing: %v", err)
	}
	if got != nil {
		t.Errorf("Read missing = %+v, want nil", got)
	}
}

// stubReadFile swaps in a fake readFile with retry enabled and a zero backoff,
// so the retry loop runs deterministically on any platform (the real sharing
// violation only reproduces on Windows). It records every call and returns
// the queued results in order, repeating the last one once they run out.
func stubReadFile(t *testing.T, results []error, payload []byte) *int {
	t.Helper()
	calls := new(int)
	origReadFile, origEnabled, origBackoff := readFile, readRetryEnabled, readBackoff
	readFile = func(string) ([]byte, error) {
		*calls++
		if *calls-1 < len(results) {
			return payload, results[*calls-1]
		}
		return payload, results[len(results)-1]
	}
	readRetryEnabled = true
	readBackoff = 0
	t.Cleanup(func() {
		readFile, readRetryEnabled, readBackoff = origReadFile, origEnabled, origBackoff
	})
	return calls
}

// sharingViolationErr stands in for the transient Windows
// ERROR_SHARING_VIOLATION ("The process cannot access the file because it is
// being used by another process") this retry exists for; the loop is
// deliberately unconditional on error identity, so any non-ErrNotExist error
// drives it. Constructed per call — a package-level sentinel var trips
// staticcheck ST1005 (capitalized error string).
func sharingViolationErr() error {
	return errors.New("The process cannot access the file because it is being used by another process")
}

const testInfoJSON = `{"pid": 4242, "port": 3001}` + "\n"

// TestReadRetriesTransientWindowsSharingViolation: a read that fails briefly
// and then succeeds (the antivirus/teardown window after the daemon writes
// running.json) must ride out the failure and return the file.
func TestReadRetriesTransientWindowsSharingViolation(t *testing.T) {
	calls := stubReadFile(t, []error{sharingViolationErr(), sharingViolationErr(), nil}, []byte(testInfoJSON))

	got, err := Read(filepath.Join(t.TempDir(), "running.json"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil || got.PID != 4242 || got.Port != 3001 {
		t.Errorf("Read = %+v, want PID=4242 Port=3001", got)
	}
	if *calls != 3 {
		t.Errorf("readFile calls = %d, want 3", *calls)
	}
}

// TestReadGivesUpAfterBudget: a genuinely wedged file still surfaces the last
// error after the bounded budget instead of hanging forever.
func TestReadGivesUpAfterBudget(t *testing.T) {
	calls := stubReadFile(t, []error{sharingViolationErr()}, nil)

	_, err := Read(filepath.Join(t.TempDir(), "running.json"))
	if err == nil {
		t.Fatal("Read succeeded against a permanently failing readFile")
	}
	if *calls != readAttempts {
		t.Errorf("readFile calls = %d, want %d (full budget)", *calls, readAttempts)
	}
}

// TestReadSkipsRetryOffWindows: the retry is gated to the platform whose
// handle semantics need it; elsewhere a read error is real and immediate.
func TestReadSkipsRetryOffWindows(t *testing.T) {
	calls := stubReadFile(t, []error{sharingViolationErr()}, nil)
	readRetryEnabled = false // stand in for a non-Windows GOOS

	_, err := Read(filepath.Join(t.TempDir(), "running.json"))
	if err == nil {
		t.Fatal("Read succeeded, want the read error surfaced")
	}
	if *calls != 1 {
		t.Errorf("readFile calls = %d, want 1 — no retry off Windows", *calls)
	}
}

// TestReadMissingIsNotRetried: ErrNotExist is the normal stopped state, not a
// transient — retrying it would slow every `ao status` on a stopped daemon.
func TestReadMissingIsNotRetried(t *testing.T) {
	notExist := &os.PathError{Op: "open", Path: "running.json", Err: os.ErrNotExist}
	calls := stubReadFile(t, []error{notExist, nil}, []byte(testInfoJSON))

	got, err := Read(filepath.Join(t.TempDir(), "running.json"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != nil {
		t.Errorf("Read = %+v, want nil for a missing file", got)
	}
	if *calls != 1 {
		t.Errorf("readFile calls = %d, want 1 — ErrNotExist must not be retried", *calls)
	}
}

func TestRemoveIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	if err := Remove(path); err != nil {
		t.Errorf("Remove on missing file: %v", err)
	}
	if err := Write(path, Info{PID: 1, Port: 2}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Errorf("Remove existing: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present after Remove")
	}
}

func TestRemoveIfOwnedDoesNotDeleteSuccessorRunfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	if err := Write(path, Info{PID: 1, Port: 3001}); err != nil {
		t.Fatalf("Write predecessor: %v", err)
	}
	if err := Write(path, Info{PID: 2, Port: 3002}); err != nil {
		t.Fatalf("Write successor: %v", err)
	}
	if err := RemoveIfOwned(path, 1); err != nil {
		t.Fatalf("RemoveIfOwned predecessor: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil || got.PID != 2 || got.Port != 3002 {
		t.Fatalf("successor runfile was removed or changed: %+v", got)
	}
	if err := RemoveIfOwned(path, 2); err != nil {
		t.Fatalf("RemoveIfOwned successor: %v", err)
	}
	if got, err := Read(path); err != nil || got != nil {
		t.Fatalf("after owner removal got=%+v err=%v", got, err)
	}
}

func TestCheckStaleDeadPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	// PID 0x7FFFFFFF is effectively guaranteed not to exist.
	if err := Write(path, Info{PID: 0x7FFFFFFF, Port: 3001}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	live, err := CheckStale(path)
	if err != nil {
		t.Fatalf("CheckStale: %v", err)
	}
	if live != nil {
		t.Errorf("CheckStale on dead PID = %+v, want nil (stale, safe to overwrite)", live)
	}
}

func TestCheckStaleLivePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	// This test process is unquestionably alive.
	if err := Write(path, Info{PID: os.Getpid(), Port: 3001}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	live, err := CheckStale(path)
	if err != nil {
		t.Fatalf("CheckStale: %v", err)
	}
	if live == nil {
		t.Fatal("CheckStale on live PID = nil, want the live Info")
		return
	}
	if live.PID != os.Getpid() {
		t.Errorf("live.PID = %d, want %d", live.PID, os.Getpid())
	}
}

func TestCheckStaleNoFile(t *testing.T) {
	live, err := CheckStale(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("CheckStale: %v", err)
	}
	if live != nil {
		t.Errorf("CheckStale with no file = %+v, want nil", live)
	}
}
