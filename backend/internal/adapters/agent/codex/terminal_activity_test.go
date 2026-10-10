package codex

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDetectTerminalActivity(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   domain.ActivityState
		ok     bool
	}{
		{
			name:   "idle composer",
			output: "\x1b[1m›\x1b[0m \x1b[2mWrite tests for @filename\x1b[0m\n\ngpt-5.6-sol low · ~/project\n",
			want:   domain.ActivityIdle,
			ok:     true,
		},
		{
			name:   "working composer",
			output: "• Working (2m 10s • esc to interrupt)\n› Add tests\n\ngpt-5.6-sol low · ~/project\n",
		},
		{
			name:   "approval picker",
			output: "› 1. Approve once\n  2. Deny\nPress enter to confirm or esc to go back\n",
		},
		{
			name:   "assistant text",
			output: "The symbol is:\n›\nnot a composer footer\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Plugin{}).DetectTerminalActivity(tt.output)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestInspectTerminalSurfaceSeparatesCodexWorkFromComposer(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantWork   ports.TerminalSurfaceWorkState
		wantEditor ports.TerminalComposerState
	}{
		{
			name:       "idle empty composer",
			output:     "\x1b[1m›\x1b[0m \x1b[2mWrite tests for @filename\x1b[0m\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "current Codex placeholder without dim styling",
			output:     "› Ask Codex to do anything\n\nGPT-6-Sol medium · ~/project\n? for shortcuts\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "text appended to Codex placeholder remains a draft",
			output:     "› Ask Codex to do anything else\n\nGPT-6-Sol medium · ~/project\n? for shortcuts\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "idle empty composer when constrained viewport hides footer",
			output:     "\x1b[2m• \x1b[0mE2E_ROUNDTRIP_TWO\n\n\n\x1b[1m›\x1b[0m\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "plain transcript prompt without footer is not current chrome",
			output:     "The response ended with an example:\n›\n",
			wantWork:   ports.TerminalSurfaceWorkUnknown,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "active empty composer when constrained viewport hides footer",
			output:     "\x1b[2m• Working (4s • esc to interrupt)\x1b[0m\n\n\x1b[1m›\x1b[0m\n",
			wantWork:   ports.TerminalSurfaceWorkActive,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "idle empty composer above a two-row footer",
			output: "\x1b[1m›\x1b[m \x1b[2mAsk Codex to do anything\x1b[m\n\n" +
				"  \x1b[38;2;246;226;183mGPT-6.1-Sol low\x1b[m · \x1b[38;2;171;223;167m~/project\x1b[m\n" +
				"  \x1b[1m←\x1b[m for agents · \x1b[1m?\x1b[m for shortcuts    ⚠ \x1b[38;2;196;167;103m3 warnings\x1b[m · \x1b[1mf2\x1b[m to view\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "idle draft above a two-row footer",
			output: "\x1b[1m›\x1b[m Keep this draft\n\n" +
				"  \x1b[38;2;246;226;183mGPT-6.1-Sol low\x1b[m · \x1b[38;2;171;223;167m~/project\x1b[m\n" +
				"  \x1b[1m?\x1b[m for shortcuts    ⚠ 3 warnings · \x1b[1mf2\x1b[m to view\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "idle draft",
			output:     "› Keep this draft\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "active wording inside draft is not current chrome",
			output:     "› Quote esc to interrupt here\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "active empty composer",
			output:     "• Working (2m 10s • esc to interrupt)\n› \x1b[2mAdd tests\x1b[0m\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkActive,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "old active row in transcript is not current chrome",
			output: "• Working (2m 10s • esc to interrupt)\nThe work finished.\n" +
				"› \x1b[2mAdd tests\x1b[0m\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "approval picker",
			output:     "› 1. Approve once\n  2. Deny\nPress enter to confirm or esc to go back\n",
			wantWork:   ports.TerminalSurfaceWorkWaitingInput,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "approval picker with normal footer still visible",
			output: "Run this command?\n› 1. Approve once\n  2. Deny\n" +
				"Press enter to confirm or esc to go back\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkWaitingInput,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name:       "approval picker with a non-first selected option",
			output:     "Run this command?\n  1. Approve once\n› 2. Deny\nPress enter to confirm or esc to go back\n",
			wantWork:   ports.TerminalSurfaceWorkWaitingInput,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "completed approval picker above the current composer is idle",
			output: "Run this command?\n› 1. Approve once\n  2. Deny\nPress enter to confirm or esc to go back\n" +
				"› \x1b[2mAdd tests\x1b[0m\n\ngpt-5.6-sol low · ~/project\n",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&Plugin{}).InspectTerminalSurface(tt.output)
			if got.Work != tt.wantWork || got.Composer != tt.wantEditor {
				t.Fatalf("InspectTerminalSurface() = %+v, want work=%v composer=%v", got, tt.wantWork, tt.wantEditor)
			}
		})
	}
}

func TestInspectTerminalSurfaceOnlyProvesAnUnstartedConversationOnInitialFrame(t *testing.T) {
	header := "╭────────────────────────╮\n│ >_ OpenAI Codex (v0.147.0) │\n╰────────────────────────╯\n\nTip: Try the Desktop app.\n"
	footer := "\n\ngpt-5.6-sol low · ~/project\n"
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "initial empty composer",
			output: header + "\n\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m" + footer,
			want:   true,
		},
		{
			name:   "initial composer with unsent draft",
			output: header + "\n\x1b[1m›\x1b[0m Keep this draft" + footer,
			want:   true,
		},
		{
			name: "completed turn",
			output: header + "\n› Say hello\n\n• Hello\n\n" +
				"\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m" + footer,
		},
		{
			name:   "partial frame without provider header",
			output: "\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m" + footer,
		},
		{
			name:   "active turn",
			output: header + "\n• Working (4s • esc to interrupt)\n\x1b[1m›\x1b[0m" + footer,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&Plugin{}).InspectTerminalSurface(tt.output)
			if got.NativeConversationNotStarted != tt.want {
				t.Fatalf("NativeConversationNotStarted = %v, want %v; observation=%+v", got.NativeConversationNotStarted, tt.want, got)
			}
		})
	}
}

func TestCodexWarningFooterDoesNotBecomeComposerDraft(t *testing.T) {
	for _, footer := range []string{
		"? for shortcuts                         ⚠ 3 warnings · f2 to view",
		"⚠ 2 warnings · f2 to view",
		"inline",
		"\x1b[1m?\x1b[m for shortcuts  ⚠ \x1b[38;2;196;167;103m3 warnings\x1b[m · \x1b[1mf2\x1b[m to view",
	} {
		for _, tc := range []struct {
			name, prompt string
			want         ports.TerminalComposerState
		}{
			{"empty", "\x1b[1m›\x1b[m \x1b[2mAsk Codex to do anything\x1b[m", ports.TerminalComposerEmpty},
			{"draft", "\x1b[1m›\x1b[m Keep my draft", ports.TerminalComposerDraft},
			{"wrapped draft", "\x1b[1m›\x1b[m Keep my draft\n  and its next line", ports.TerminalComposerDraft},
			{"middle dot in draft", "\x1b[1m›\x1b[m \x1b[2mAsk Codex to do anything\x1b[m\n  preserve · this text", ports.TerminalComposerDraft},
		} {
			t.Run(tc.name+"/"+footer, func(t *testing.T) {
				output := "\x1b[2m•\x1b[m Previous turn completed.\n\n" + tc.prompt + "\n\n  \x1b[38;2;246;226;183mGPT-5.5 low\x1b[m · ~/project · Task title\n  " + footer
				if footer == "inline" {
					output = "\x1b[2m•\x1b[m Previous turn completed.\n\n" + tc.prompt + "\n\n  GPT-5.5 low · ~/project · Task title     ⚠ 2 warnings · f2 to view"
				}
				got := (&Plugin{}).InspectTerminalSurface(output)
				if got.Work != ports.TerminalSurfaceWorkIdle || got.Composer != tc.want {
					t.Fatalf("warning footer contaminated composer: %+v, want idle/%v", got, tc.want)
				}
				active := strings.Replace(output, "Previous turn completed.", "Working (4s • esc to interrupt)", 1)
				got = (&Plugin{}).InspectTerminalSurface(active)
				if got.Work != ports.TerminalSurfaceWorkActive || got.Composer != tc.want {
					t.Fatalf("warning footer hid active work or changed draft: %+v", got)
				}
			})
		}
	}
}

func TestCodexPromptGlyphCompatibilityPreservesMutationSafety(t *testing.T) {
	footer := "\n\n  GPT-5.5 low · ~/project · Task title\n  ? for shortcuts  ⚠ 3 warnings · f2 to view\n"
	cases := []struct {
		name, frame string
		work        ports.TerminalSurfaceWorkState
		composer    ports.TerminalComposerState
	}{
		{
			name:  "idle placeholder with warning footer",
			frame: "\x1b[2m•\x1b[m Finished.\n\n\x1b[1mPROMPT\x1b[m \x1b[2mAsk Codex to do anything\x1b[m" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "plain provider placeholder after handoff",
			frame: "PROMPT Ask Codex to do anything" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "unsent draft remains protected",
			frame: "\x1b[1mPROMPT\x1b[m Keep this draft" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerDraft,
		},
		{
			name:  "wrapped draft remains protected",
			frame: "\x1b[1mPROMPT\x1b[m Keep this draft\n  and this line" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerDraft,
		},
		{
			name:  "active turn is not idle",
			frame: "• Working (4s • esc to interrupt)\n\x1b[1mPROMPT\x1b[m \x1b[2mAsk Codex to do anything\x1b[m" + footer,
			work:  ports.TerminalSurfaceWorkActive, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "active constrained viewport",
			frame: "• Working (4s • esc to interrupt)\n\n\x1b[1mPROMPT\x1b[m\n",
			work:  ports.TerminalSurfaceWorkActive, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "idle constrained viewport",
			frame: "\x1b[2m•\x1b[m Finished.\n\n\x1b[1mPROMPT\x1b[m\n",
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "plain transcript cannot establish idle",
			frame: "Example symbol:\nPROMPT\n",
			work:  ports.TerminalSurfaceWorkUnknown, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "dim transcript cannot establish idle",
			frame: "Example symbol:\n\x1b[1;2mPROMPT\x1b[m\n",
			work:  ports.TerminalSurfaceWorkUnknown, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "approval first selection remains protected",
			frame: "Run command?\nPROMPT 1. Approve once\n  2. Deny\nPress enter to confirm or esc to go back\n" + footer,
			work:  ports.TerminalSurfaceWorkWaitingInput, composer: ports.TerminalComposerUnknown,
		},
		{
			name:  "approval second selection remains protected",
			frame: "Run command?\n  1. Approve once\nPROMPT 2. Deny\nPress enter to confirm or esc to go back\n",
			work:  ports.TerminalSurfaceWorkWaitingInput, composer: ports.TerminalComposerUnknown,
		},
		{
			name:  "completed approval is scrollback",
			frame: "Run command?\nOTHER 1. Approve once\n  2. Deny\nPress enter to confirm or esc to go back\nPROMPT \x1b[2mAsk Codex to do anything\x1b[m" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "current prompt wins over different transcript glyph",
			frame: "OTHER Previous human input\n• Done.\n\n\x1b[1mPROMPT\x1b[m" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerEmpty,
		},
		{
			name:  "glyph inside draft does not hide draft",
			frame: "\x1b[1mPROMPT\x1b[m Explain OTHER and PROMPT" + footer,
			work:  ports.TerminalSurfaceWorkIdle, composer: ports.TerminalComposerDraft,
		},
	}
	for _, marker := range []string{"›", "»"} {
		other := "»"
		if marker == other {
			other = "›"
		}
		for _, tc := range cases {
			t.Run(marker+"/"+tc.name, func(t *testing.T) {
				frame := strings.NewReplacer("PROMPT", marker, "OTHER", other).Replace(tc.frame)
				got := (&Plugin{}).InspectTerminalSurface(frame)
				if got.Work != tc.work || got.Composer != tc.composer {
					t.Fatalf("observation=%+v, want work=%v composer=%v", got, tc.work, tc.composer)
				}
				state, idle := (&Plugin{}).DetectTerminalActivity(frame)
				if idle != (tc.work == ports.TerminalSurfaceWorkIdle) || (idle && state != domain.ActivityIdle) {
					t.Fatalf("idle classification=(%v,%v), work=%v", state, idle, tc.work)
				}
			})
		}
	}
}

func TestCodexPromptGlyphCompatibilityPreservesConversationStartedProof(t *testing.T) {
	header := "│ >_ OpenAI Codex (v0.160.0) │\n"
	footer := "\n\nGPT-5.5 low · ~/project\n"
	for _, marker := range []string{"›", "»"} {
		for _, history := range []string{"", "› Previous request\n• Done.\n", "» Previous request\n• Done.\n"} {
			t.Run(marker+"/"+history, func(t *testing.T) {
				frame := header + history + "\x1b[1m" + marker + "\x1b[m \x1b[2mAsk Codex to do anything\x1b[m" + footer
				got := (&Plugin{}).InspectTerminalSurface(frame)
				if got.NativeConversationNotStarted != (history == "") {
					t.Fatalf("initial-conversation proof=%v, history=%q", got.NativeConversationNotStarted, history)
				}
			})
		}
	}
}
