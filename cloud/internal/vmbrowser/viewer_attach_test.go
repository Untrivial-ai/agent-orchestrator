package vmbrowser

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
)

func TestViewerAttachKeepsEngineSelectedTab(t *testing.T) {
	cdp := &viewerTestCDP{engine: &viewerTestEngine{}, targets: []viewerTestTarget{
		{id: "cdp-1", title: "First", url: "https://one.example.test/"},
		{id: "cdp-2", title: "Second", url: "https://two.example.test/"},
	}}
	engine := &viewerTabEngine{
		active: "tab-2", tabs: []string{"tab-2", "tab-1"}, cdp: cdp,
		urls:   map[string]string{"tab-1": "https://one.example.test/", "tab-2": "https://two.example.test/"},
		titles: map[string]string{"tab-1": "First", "tab-2": "Second"},
	}
	viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
	for _, active := range []string{"tab-2", "tab-1"} {
		engine.active = active
		state := &viewerSession{
			ctx: context.Background(), cdp: cdp, control: make(chan browserstream.Control, 16),
			frames: browserstream.NewLatest(), width: 1280, height: 720,
		}
		if err := viewer.attachInitialTarget(state); err != nil {
			t.Fatal(err)
		}
		want := "cdp-2"
		if active == "tab-1" {
			want = "cdp-1"
		}
		if state.targetID != want || engine.active != active {
			t.Fatalf("viewer=%s engine=%s want=%s", state.targetID, engine.active, want)
		}
		state.frames.Close()
		cdp.targets[0], cdp.targets[1] = cdp.targets[1], cdp.targets[0]
	}
}

func TestViewerAttachRejectsAmbiguousTabIdentity(t *testing.T) {
	cdp := &viewerTestCDP{targets: []viewerTestTarget{{id: "one", url: "about:blank"}, {id: "two", url: "about:blank"}}}
	engine := &viewerTabEngine{
		active: "tab-2", tabs: []string{"tab-1", "tab-2"},
		urls: map[string]string{"tab-1": "about:blank", "tab-2": "about:blank"},
	}
	viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
	state := &viewerSession{ctx: context.Background(), cdp: cdp}
	if err := viewer.attachInitialTarget(state); err == nil {
		t.Fatal("ambiguous tabs were paired by position")
	}
	if cdp.called("Target.activateTarget") {
		t.Fatal("ambiguous attach changed the selected page")
	}
}

func TestViewerAttachMatchesUniqueURLWithDifferentTitles(t *testing.T) {
	for _, tt := range []struct {
		name        string
		secondURL   string
		engineTitle string
		wantError   bool
	}{
		{name: "distinct URLs", secondURL: "https://two.example.test/", engineTitle: "two.example.test"},
		{name: "duplicate URLs", secondURL: "https://one.example.test/", engineTitle: "one.example.test", wantError: true},
		{name: "duplicate URLs with one matching title", secondURL: "https://one.example.test/", engineTitle: "Example Domain", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cdp := &viewerTestCDP{engine: &viewerTestEngine{}, targets: []viewerTestTarget{
				{id: "cdp-1", title: "Example Domain", url: "https://one.example.test/"},
				{id: "cdp-2", title: "Example Domain", url: tt.secondURL},
			}}
			engine := &viewerTabEngine{
				active: "tab-2", tabs: []string{"tab-2", "tab-1"}, cdp: cdp,
				urls:   map[string]string{"tab-1": "https://one.example.test/", "tab-2": tt.secondURL},
				titles: map[string]string{"tab-1": "one.example.test", "tab-2": tt.engineTitle},
			}
			viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
			state := &viewerSession{
				ctx: context.Background(), cdp: cdp, control: make(chan browserstream.Control, 16),
				frames: browserstream.NewLatest(), width: 1280, height: 720,
			}
			defer state.frames.Close()
			err := viewer.attachInitialTarget(state)
			if tt.wantError {
				if err == nil {
					t.Fatal("ambiguous URL was matched to a target")
				}
				if cdp.called("Target.activateTarget") {
					t.Fatal("ambiguous attach changed the selected page")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.targetID != "cdp-2" || engine.active != "tab-2" {
				t.Fatalf("viewer=%s engine=%s", state.targetID, engine.active)
			}
		})
	}
}
