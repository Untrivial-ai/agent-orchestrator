package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watcherTestTimeout = 5 * time.Second

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptWatcherExistingRootWrite(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "session.jsonl")
	mustNoError(t, os.WriteFile(transcript, []byte("{\"type\":\"message\"}\n"), 0o600))
	watcher, cancel := startTranscriptWatcher(t, root, transcript)
	defer stopTranscriptWatcher(t, watcher, cancel)

	mustNoError(t, os.WriteFile(transcript, []byte("{\"type\":\"message\",\"updated\":true}\n"), 0o600))
	waitForTranscriptEvent(t, watcher.Events(), transcript)

	ignored := filepath.Join(root, "notes.txt")
	mustNoError(t, os.WriteFile(ignored, []byte("not a transcript"), 0o600))
	assertNoTranscriptEvent(t, watcher.Events(), ignored)
}

func TestTranscriptWatcherErrorsRedactRootPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-transcript-root")
	mustNoError(t, os.WriteFile(root, []byte("not a directory"), 0o600))
	_, err := NewTranscriptWatcher(context.Background(), []string{root})
	if err == nil {
		t.Fatal("expected file transcript root to be rejected")
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("watcher error exposed transcript root: %v", err)
	}
}

func TestTranscriptWatcherDoesNotWatchUnrelatedHistory(t *testing.T) {
	root := t.TempDir()
	watcher, err := NewTranscriptWatcher(context.Background(), []string{root})
	mustNoError(t, err)
	t.Cleanup(watcher.close)
	for index := range 2_000 {
		mustNoError(t, os.Mkdir(filepath.Join(root, fmt.Sprintf("history-%04d", index)), 0o700))
	}
	mustNoError(t, watcher.Rebuild(context.Background(), nil))
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if len(watcher.watched) != 0 {
		t.Fatalf("watched %d paths, want no unrelated history", len(watcher.watched))
	}
}

func TestTranscriptWatcherAddsOnlyRegisteredSources(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, "history", "old.jsonl")
	sourceDir := filepath.Join(root, "2026", "08", "06")
	mustNoError(t, os.MkdirAll(filepath.Dir(unrelated), 0o700))
	mustNoError(t, os.MkdirAll(sourceDir, 0o700))
	mustNoError(t, os.WriteFile(unrelated, []byte("{}\n"), 0o600))
	source := filepath.Join(sourceDir, "rollout.jsonl")
	mustNoError(t, os.WriteFile(source, []byte("{}\n"), 0o600))
	watcher, err := NewTranscriptWatcher(context.Background(), []string{root})
	mustNoError(t, err)
	t.Cleanup(watcher.close)

	mustNoError(t, watcher.Rebuild(context.Background(), []string{source}))
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if _, ok := watcher.watched[canonicalTranscriptPath(source)]; !ok {
		t.Fatalf("active source %q is not watched", source)
	}
	if _, ok := watcher.watched[unrelated]; ok {
		t.Fatalf("unrelated history source %q is watched", unrelated)
	}
}

func TestTranscriptWatcherResolvesSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "transcripts")
	mustNoError(t, os.Mkdir(root, 0o700))
	link := filepath.Join(base, "linked-transcripts")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(link)
	mustNoError(t, err)

	transcript := filepath.Join(resolvedRoot, "session.jsonl")
	mustNoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o600))
	watcher, cancel := startTranscriptWatcher(t, link, transcript)
	defer stopTranscriptWatcher(t, watcher, cancel)
	waitForWatchedPath(t, watcher, transcript)

	mustNoError(t, os.WriteFile(transcript, []byte("{\"updated\":true}\n"), 0o600))
	waitForTranscriptEvent(t, watcher.Events(), transcript)
}

func TestTranscriptWatcherAddsSourceWhenRetryFindsMissingFile(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "rollout.jsonl")
	watcher, err := NewTranscriptWatcher(context.Background(), []string{root})
	mustNoError(t, err)
	t.Cleanup(watcher.close)

	mustNoError(t, watcher.Rebuild(context.Background(), []string{transcript}))
	watcher.mu.Lock()
	_, watched := watcher.watched[transcript]
	watcher.mu.Unlock()
	if watched {
		t.Fatal("missing transcript was watched")
	}

	mustNoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o600))
	mustNoError(t, watcher.Rebuild(context.Background(), []string{transcript}))
	waitForWatchedPath(t, watcher, transcript)
}

func TestTranscriptWatcherEmitsRenameAndRewatchesReplacement(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "rollout.jsonl")
	mustNoError(t, os.WriteFile(transcript, []byte("{\"generation\":1}\n"), 0o600))
	watcher, cancel := startTranscriptWatcher(t, root, transcript)
	defer stopTranscriptWatcher(t, watcher, cancel)

	renamed := filepath.Join(root, "rollout.old")
	mustNoError(t, os.Rename(transcript, renamed))
	waitForTranscriptEvent(t, watcher.Events(), transcript)

	mustNoError(t, os.WriteFile(transcript, []byte("{\"generation\":2}\n"), 0o600))
	mustNoError(t, watcher.Rebuild(context.Background(), []string{transcript}))
	mustNoError(t, os.WriteFile(transcript, []byte("{\"generation\":3}\n"), 0o600))
	waitForTranscriptEvent(t, watcher.Events(), transcript)
}

func TestTranscriptWatcherCleanShutdown(t *testing.T) {
	root := t.TempDir()
	watcher, cancel := startTranscriptWatcher(t, root)
	cancel()

	select {
	case <-watcher.Start(context.Background()):
	case <-time.After(watcherTestTimeout):
		t.Fatal("watcher did not stop after context cancellation")
	}
	waitForClosedTranscriptEvents(t, watcher.Events())
	waitForClosedWatcherErrors(t, watcher.Errors())

	if err := watcher.Rebuild(context.Background(), nil); err == nil {
		t.Fatal("Rebuild() after shutdown succeeded, want closed error")
	}
}

func TestTranscriptWatcherMarksOnlyCreationEventsForDiscovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	mustNoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	watcher, err := NewTranscriptWatcher(context.Background(), []string{root})
	mustNoError(t, err)
	t.Cleanup(watcher.close)

	emit, discovery, err := watcher.handleEvent(context.Background(), fsnotify.Event{Name: path, Op: fsnotify.Create})
	if err != nil || canonicalTranscriptPath(emit) != canonicalTranscriptPath(path) || !discovery {
		t.Fatalf("create event = path:%q discovery:%v err:%v", emit, discovery, err)
	}
	emit, discovery, err = watcher.handleEvent(context.Background(), fsnotify.Event{Name: path, Op: fsnotify.Write})
	if err != nil || canonicalTranscriptPath(emit) != canonicalTranscriptPath(path) || discovery {
		t.Fatalf("write event = path:%q discovery:%v err:%v", emit, discovery, err)
	}
}

func startTranscriptWatcher(t *testing.T, root string, sourcePaths ...string) (*TranscriptWatcher, context.CancelFunc) {
	t.Helper()
	watcher, err := NewTranscriptWatcher(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("NewTranscriptWatcher() error = %v", err)
	}
	if err := watcher.Rebuild(context.Background(), sourcePaths); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher.Start(ctx)
	return watcher, cancel
}

func stopTranscriptWatcher(t *testing.T, watcher *TranscriptWatcher, cancel context.CancelFunc) {
	t.Helper()
	cancel()
	select {
	case <-watcher.Start(context.Background()):
	case <-time.After(watcherTestTimeout):
		t.Fatal("watcher did not stop")
	}
}

func waitForTranscriptEvent(t *testing.T, events <-chan TranscriptEvent, path string) {
	t.Helper()
	path = canonicalTranscriptPath(path)
	timer := time.NewTimer(watcherTestTimeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("events closed while waiting for %q", path)
			}
			if canonicalTranscriptPath(event.Path) == path {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for transcript event %q", path)
		}
	}
}

func assertNoTranscriptEvent(t *testing.T, events <-chan TranscriptEvent, path string) {
	t.Helper()
	path = canonicalTranscriptPath(path)
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if canonicalTranscriptPath(event.Path) == path {
				t.Fatalf("received event for non-transcript path %q", path)
			}
		case <-timer.C:
			return
		}
	}
}

func waitForWatchedPath(t *testing.T, watcher *TranscriptWatcher, path string) {
	t.Helper()
	path = canonicalTranscriptPath(path)
	deadline := time.Now().Add(watcherTestTimeout)
	for time.Now().Before(deadline) {
		watcher.mu.Lock()
		_, ok := watcher.watched[path]
		watcher.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("path %q was not added to the watch set", path)
}

func waitForClosedTranscriptEvents(t *testing.T, events <-chan TranscriptEvent) {
	t.Helper()
	timer := time.NewTimer(watcherTestTimeout)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("events channel did not close")
		}
	}
}

func waitForClosedWatcherErrors(t *testing.T, errors <-chan error) {
	t.Helper()
	timer := time.NewTimer(watcherTestTimeout)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-errors:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("errors channel did not close")
		}
	}
}
