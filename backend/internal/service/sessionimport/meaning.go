package sessionimport

import "strings"

// Meaning is the verdict on whether an on-disk conversation is worth importing.
//
// AO never imports a conversation it judges trivial: a greeting, an empty or
// aborted attempt, a failed login, or a throwaway smoke test. Message count is
// deliberately not the signal — a short but productive coding session must
// survive, and a long aimless one need not. What matters is whether the
// conversation contains real work: a task, a decision, an investigation, an
// implementation, or a substantive discussion.
type Meaning string

const (
	// MeaningTrivial is a conversation with nothing worth keeping. Discovery
	// drops it, so it never reaches the sidebar, the board, or the import list.
	MeaningTrivial Meaning = "trivial"
	// MeaningAmbiguous is a conversation the local heuristic cannot place with
	// confidence. It is kept: losing real work is worse than showing one extra
	// row. This is the bucket a provider-side classifier resolves in stage two.
	MeaningAmbiguous Meaning = "ambiguous"
	// MeaningMeaningful is a conversation containing real work.
	MeaningMeaningful Meaning = "meaningful"
)

// Signals are the cheap, provider-agnostic facts one transcript scan yields.
// Every Source fills the same struct so classification stays identical across
// Claude Code, Codex, and any provider added later.
type Signals struct {
	// UserMessages counts typed human turns, excluding tool results.
	UserMessages int
	// AssistantMessages counts agent replies.
	AssistantMessages int
	// ToolCalls counts file edits, shell commands, and other tool invocations.
	ToolCalls int
	// FirstPrompt is the first typed human turn, used to recognize greetings.
	FirstPrompt string
	// AuthFailure is true when the transcript carries a provider auth error.
	AuthFailure bool
	// Scanned is false when the transcript was too large to read in full, in
	// which case the counts above are incomplete.
	Scanned bool
}

// Classify turns transcript signals into an import verdict. The order of the
// rules matters: each one is a reason to stop asking further questions.
func Classify(s Signals) Meaning {
	// Too large to scan means substantial by construction — nothing that big is
	// a greeting. Judging it on truncated counts would throw away real work.
	if !s.Scanned {
		return MeaningMeaningful
	}
	// Nothing was ever typed: there is no conversation here at all.
	if s.UserMessages == 0 {
		return MeaningTrivial
	}
	// A prompt that never got a reply is an abandoned attempt — but only junk if
	// the prompt was junk. Real transcripts include detailed requests, some with
	// attachments, that the agent never answered; those are unfinished work and
	// are exactly what someone would want to pick back up.
	if s.AssistantMessages == 0 {
		if isThrowawayPrompt(s.FirstPrompt) || !isSubstantialPrompt(s.FirstPrompt) {
			return MeaningTrivial
		}
		return MeaningMeaningful
	}
	// A session that only ever failed to authenticate produced no work. Gated on
	// being short and tool-free so a genuine debugging session about auth, which
	// quotes the same errors, is not mistaken for one.
	if s.AuthFailure && s.ToolCalls == 0 && s.UserMessages <= 2 {
		return MeaningTrivial
	}
	// Files edited, commands run, tools used: real work regardless of length.
	// This is what keeps a short but productive coding session.
	if s.ToolCalls > 0 {
		return MeaningMeaningful
	}
	// A greeting or smoke test that never turned into a conversation.
	if isThrowawayPrompt(s.FirstPrompt) && s.UserMessages < 3 {
		return MeaningTrivial
	}
	// A real back-and-forth with no tool use is still meaningful: substantial
	// discussion and research sessions are imported, by explicit product intent.
	if s.UserMessages >= 2 {
		return MeaningMeaningful
	}
	// One exchange, no tools. Meaningful only if the prompt itself carries
	// weight — a pasted stack trace or a detailed question, not a one-liner.
	if isSubstantialPrompt(s.FirstPrompt) {
		return MeaningMeaningful
	}
	return MeaningAmbiguous
}

// mergeSignals folds one segment's signals into a conversation's running
// totals. Providers that split a conversation across resume segments call this
// per segment. Counts take the maximum rather than the sum, because resume
// segments replay earlier turns and summing would inflate a short conversation
// into a long one. Evidence of work and of auth failure is sticky: seen once in
// any segment, it holds for the conversation.
func mergeSignals(into, seg Signals) Signals {
	if seg.UserMessages > into.UserMessages {
		into.UserMessages = seg.UserMessages
	}
	if seg.AssistantMessages > into.AssistantMessages {
		into.AssistantMessages = seg.AssistantMessages
	}
	if seg.ToolCalls > into.ToolCalls {
		into.ToolCalls = seg.ToolCalls
	}
	into.AuthFailure = into.AuthFailure || seg.AuthFailure
	// One unscannable segment makes the whole conversation unscanned: the counts
	// are known to be incomplete, so it must not be judged on them.
	into.Scanned = into.Scanned && seg.Scanned
	if into.FirstPrompt == "" {
		into.FirstPrompt = seg.FirstPrompt
	}
	return into
}

// Imported reports whether this verdict should reach the user. Trivial is the
// only verdict that is withheld.
func (m Meaning) Imported() bool { return m != MeaningTrivial }

// throwawayPrompts are openings that carry no request. Matched only against a
// normalized whole prompt, never as a substring, so "test the login flow" is
// unaffected by the presence of "test".
var throwawayPrompts = map[string]struct{}{
	"hi": {}, "hii": {}, "hey": {}, "hey there": {}, "hi there": {},
	"hello": {}, "hello there": {}, "hello world": {}, "hola": {},
	"yo": {}, "sup": {}, "good morning": {}, "good evening": {},
	"test": {}, "testing": {}, "test test": {}, "just testing": {},
	"ping": {}, "asdf": {}, "1": {}, "123": {}, "abc": {},
	"ok": {}, "okay": {}, "thanks": {}, "thank you": {}, "ty": {},
	"are you there": {}, "you there": {}, "can you hear me": {},
	"who are you": {}, "what can you do": {}, "help": {},
}

// isThrowawayPrompt reports whether a prompt is a greeting or smoke test rather
// than a request. An empty prompt counts: there is nothing to justify keeping.
func isThrowawayPrompt(prompt string) bool {
	p := normalizePrompt(prompt)
	if p == "" {
		return true
	}
	if _, ok := throwawayPrompts[p]; ok {
		return true
	}
	// A few characters with nothing technical in them is a smoke test. The
	// technical check is what protects a terse but real request like "fix ci".
	return len(p) <= 12 && !hasTechnicalMarker(prompt)
}

// isSubstantialPrompt reports whether a single prompt alone justifies keeping
// the conversation.
func isSubstantialPrompt(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return false
	}
	// A multi-line prompt is pasted context: a stack trace, a diff, a spec.
	if strings.ContainsAny(trimmed, "\n\r") {
		return true
	}
	if len([]rune(trimmed)) >= 80 {
		return true
	}
	return hasTechnicalMarker(trimmed) && len([]rune(trimmed)) >= 25
}

// technicalMarkers are fragments that signal a prompt is about code or systems
// rather than conversation.
var technicalMarkers = []string{
	"/", "\\", "`", "()", "{", "$",
	".go", ".ts", ".tsx", ".js", ".py", ".rs", ".java", ".rb", ".md", ".json", ".yaml", ".sql",
	"error", "bug", "fix", "crash", "fail", "build", "compile", "deploy",
	"refactor", "implement", "debug", "stack trace", "exception",
	"http", "api", "npm", "git ", "docker", "sql", "query", "function", "class ",
}

func hasTechnicalMarker(prompt string) bool {
	p := strings.ToLower(prompt)
	for _, marker := range technicalMarkers {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}

// normalizePrompt lowercases, collapses whitespace, and strips trailing
// punctuation so "Hi!!" and "hi" compare equal.
func normalizePrompt(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.Join(strings.Fields(p), " ")
	return strings.TrimRight(p, ".!?,:; ")
}

// authFailureMarkers are provider messages that mean the session never got off
// the ground. Compared case-insensitively against raw transcript lines.
var authFailureMarkers = []string{
	"invalid api key",
	"invalid_api_key",
	"authentication_error",
	"authentication error",
	"401 unauthorized",
	"please run /login",
	"credit balance is too low",
	"oauth token has expired",
	"you are not logged in",
}

// hasAuthFailureMarker reports whether a transcript line carries a provider
// auth error. It takes an already-lowercased line so the caller can lowercase
// once per line instead of once per marker.
func hasAuthFailureMarker(lowerLine string) bool {
	for _, marker := range authFailureMarkers {
		if strings.Contains(lowerLine, marker) {
			return true
		}
	}
	return false
}
