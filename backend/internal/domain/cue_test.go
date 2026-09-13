package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestCueTypeValid pins the supported cue types.
func TestCueTypeValid(t *testing.T) {
	tests := []struct {
		typ   CueType
		valid bool
	}{
		{CueTypeCommand, true},
		{CueTypeAgent, true},
		{CueType(""), false},
		{CueType("prompt"), false},
		{CueType("Command"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			if got := tt.typ.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// TestCueValidateValid accepts a minimum viable command and agent cue and keeps
// the size bounds boundary-exact (at the cap is valid, one over is rejected).
func TestCueValidateValid(t *testing.T) {
	command := Cue{
		ID:          "cue-1",
		ProjectID:   "proj",
		Name:        "Test",
		Description: "Run the test suite",
		Type:        CueTypeCommand,
		Command:     "pnpm test",
	}
	if err := command.Validate(); err != nil {
		t.Errorf("command cue Validate() = %v, want nil", err)
	}

	agent := Cue{
		ID:          "cue-2",
		ProjectID:   "proj",
		Name:        "Fix Tests",
		Description: "Run tests and fix failures",
		Type:        CueTypeAgent,
		Prompt:      "Run the test suite, investigate any failures, and fix them.",
	}
	if err := agent.Validate(); err != nil {
		t.Errorf("agent cue Validate() = %v, want nil", err)
	}

	command.Command = strings.Repeat("x", MaxCueCommandLength)
	if err := command.Validate(); err != nil {
		t.Errorf("command at cap Validate() = %v, want nil", err)
	}

	agent.Prompt = strings.Repeat("a", MaxCuePromptLength)
	if err := agent.Validate(); err != nil {
		t.Errorf("prompt at cap Validate() = %v, want nil", err)
	}

	command.Name = strings.Repeat("n", MaxCueNameLength)
	if err := command.Validate(); err != nil {
		t.Errorf("name at cap Validate() = %v, want nil", err)
	}

	command.Description = strings.Repeat("d", MaxCueDescriptionLength)
	if err := command.Validate(); err != nil {
		t.Errorf("description at cap Validate() = %v, want nil", err)
	}
}

// TestCueValidateRejectsInvalid pins every invalid shape in the validation
// matrix: missing/whitespace/oversized name, oversized description, unknown
// type, and each type missing or oversizing its executable payload.
func TestCueValidateRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		cue     Cue
		wantErr error
	}{
		{
			"missing name",
			Cue{ProjectID: "proj", Type: CueTypeCommand, Command: "pnpm test"},
			ErrInvalidCueName,
		},
		{
			"whitespace name",
			Cue{ProjectID: "proj", Name: "   ", Type: CueTypeCommand, Command: "pnpm test"},
			ErrInvalidCueName,
		},
		{
			"oversized name",
			Cue{ProjectID: "proj", Name: strings.Repeat("n", MaxCueNameLength+1), Type: CueTypeCommand, Command: "pnpm test"},
			ErrInvalidCueName,
		},
		{
			"oversized description",
			Cue{ProjectID: "proj", Name: "Test", Description: strings.Repeat("d", MaxCueDescriptionLength+1), Type: CueTypeCommand, Command: "pnpm test"},
			ErrInvalidCueDescription,
		},
		{
			"unknown type",
			Cue{ProjectID: "proj", Name: "Test", Type: CueType("prompt"), Command: "pnpm test"},
			ErrInvalidCueType,
		},
		{
			"command cue missing command",
			Cue{ProjectID: "proj", Name: "Test", Type: CueTypeCommand},
			ErrInvalidCueCommand,
		},
		{
			"command cue whitespace command",
			Cue{ProjectID: "proj", Name: "Test", Type: CueTypeCommand, Command: "   "},
			ErrInvalidCueCommand,
		},
		{
			"command cue missing command despite prompt",
			Cue{ProjectID: "proj", Name: "Test", Type: CueTypeCommand, Prompt: "do it"},
			ErrInvalidCueCommand,
		},
		{
			"command cue oversized command",
			Cue{ProjectID: "proj", Name: "Test", Type: CueTypeCommand, Command: strings.Repeat("x", MaxCueCommandLength+1)},
			ErrInvalidCueCommand,
		},
		{
			"agent cue missing prompt",
			Cue{ProjectID: "proj", Name: "Fix Tests", Type: CueTypeAgent},
			ErrInvalidCuePrompt,
		},
		{
			"agent cue whitespace prompt",
			Cue{ProjectID: "proj", Name: "Fix Tests", Type: CueTypeAgent, Prompt: "   "},
			ErrInvalidCuePrompt,
		},
		{
			"agent cue missing prompt despite command",
			Cue{ProjectID: "proj", Name: "Fix Tests", Type: CueTypeAgent, Command: "pnpm test"},
			ErrInvalidCuePrompt,
		},
		{
			"agent cue oversized prompt",
			Cue{ProjectID: "proj", Name: "Fix Tests", Type: CueTypeAgent, Prompt: strings.Repeat("p", MaxCuePromptLength+1)},
			ErrInvalidCuePrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cue.Validate(); !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCueInactivePayloadBounds(t *testing.T) {
	for _, cue := range []Cue{
		{Name: "Test", Type: CueTypeCommand, Command: "echo ok", Prompt: strings.Repeat("x", MaxCuePromptLength+1)},
		{Name: "Test", Type: CueTypeAgent, Prompt: "hello", Command: strings.Repeat("x", MaxCueCommandLength+1)},
	} {
		if err := cue.Validate(); err == nil {
			t.Fatal("oversized inactive payload accepted")
		}
	}
	for _, name := range []string{strings.Repeat("é", 32), strings.Repeat("é", 33)} {
		err := (Cue{Name: name, Type: CueTypeCommand, Command: "echo ok"}).Validate()
		if (err == nil) != (len(name) <= MaxCueNameLength) {
			t.Fatalf("UTF-8 name length %d: %v", len(name), err)
		}
	}
}
