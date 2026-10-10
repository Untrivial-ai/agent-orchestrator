package cua

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingRecoveryOnlyMovesProvedNewUUID(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	r.finish = false
	startFakeRecording(t, f)
	other := filepath.Join(f.adapter.stagingDir, "d3e12b53-8277-426c-a1cd-04a549a0389c.mov")
	if err := os.WriteFile(other, []byte("another recorder"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.process.signal = func(signal os.Signal) error {
		r.signals = append(r.signals, signal)
		r.process.err = errors.New("final move failed")
		close(r.process.done)
		return nil
	}
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if executable == "/usr/bin/log" {
			predicate := args[len(args)-1]
			if !strings.Contains(predicate, "processID == 42") || !strings.Contains(predicate, r.path) {
				t.Fatalf("failure log was not recorder/destination scoped: %v", args)
			}
			return Output{Stdout: []byte("Failed to move screen recording from " + r.staged + " to " + r.path + ". Error Code=516")}, nil
		}
		return provider(executable, args)
	}
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err != nil || result.Duration <= 0 || result.StagingPath != r.staged || !strings.Contains(result.StagingCleanup, "recovered into evidence") {
		t.Fatalf("failed staging movie was not recovered: %+v %v", result, err)
	}
	if err := verifyStagingAbsent(r.staged); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "another recorder" {
		t.Fatalf("unmatched movie changed: %q %v", data, err)
	}
}

func TestStagingUnprovedCandidateIsLeftAndJournaled(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	r.finish = false
	startFakeRecording(t, f)
	r.process.signal = func(_ os.Signal) error {
		if err := os.WriteFile(r.path, []byte("different inode"), 0o600); err != nil {
			return err
		}
		close(r.process.done)
		return nil
	}
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err != nil || !strings.HasPrefix(result.StagingCleanup, "unresolved:") || result.StagingPath != "" {
		t.Fatalf("unproved candidate was not journaled: %+v %v", result, err)
	}
	if data, err := os.ReadFile(r.staged); err != nil || string(data) != "native movie staging" {
		t.Fatalf("unproved candidate changed: %q %v", data, err)
	}
}

func TestFailedStagingLogRequiresExactDestinationAndOneUUID(t *testing.T) {
	dir, destination := "/native-staging", "/evidence/window.mov"
	line := "Failed to move screen recording from /native-staging/b97e0c9f-1aac-4fe5-bd3e-18c05c7db732.mov to /evidence/window.mov. Error"
	if failedStagingPath(line, dir, destination) == "" {
		t.Fatal("proved UUID missing")
	}
	for _, bad := range []string{
		strings.ReplaceAll(line, destination, "/evidence/other.mov"),
		strings.ReplaceAll(line, "b97e0c9f-1aac-4fe5-bd3e-18c05c7db732", "../outside"),
		line + "\n" + strings.ReplaceAll(line, "b97e0c9f-1aac-4fe5-bd3e-18c05c7db732", "d3e12b53-8277-426c-a1cd-04a549a0389c"),
	} {
		if failedStagingPath(bad, dir, destination) != "" {
			t.Fatalf("unproved failure source accepted: %s", bad)
		}
	}
}
