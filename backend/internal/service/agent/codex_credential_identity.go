package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// codexCredentialIdentity contains only locally parsed matching material. The
// API key is kept in memory for direct comparison and is never persisted,
// logged, or exposed through the API.
type codexCredentialIdentity struct {
	Method            domain.CodexAuthMethod
	ProviderAccountID string
	APIKey            string
}

func parseCodexCredentialIdentity(data []byte) (codexCredentialIdentity, error) {
	var document struct {
		OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
		Tokens       *struct {
			AccountID    string `json:"account_id"`
			AccessToken  string `json:"access_token"`
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return codexCredentialIdentity{}, errors.New("codex credential is not valid JSON")
	}
	if document.OpenAIAPIKey != nil && strings.TrimSpace(*document.OpenAIAPIKey) != "" {
		return codexCredentialIdentity{Method: domain.CodexAuthMethodAPIKey, APIKey: *document.OpenAIAPIKey}, nil
	}
	if document.Tokens == nil || (strings.TrimSpace(document.Tokens.AccessToken) == "" && strings.TrimSpace(document.Tokens.IDToken) == "" && strings.TrimSpace(document.Tokens.RefreshToken) == "") {
		return codexCredentialIdentity{}, errors.New("codex credential does not contain a supported login")
	}
	accountID := strings.TrimSpace(document.Tokens.AccountID)
	if accountID != "" && !safeProviderAccountID(accountID) {
		return codexCredentialIdentity{}, errors.New("codex credential contains an invalid account identity")
	}
	return codexCredentialIdentity{Method: domain.CodexAuthMethodChatGPT, ProviderAccountID: accountID}, nil
}

func inspectCodexCredentialIdentity(data []byte) (codexCredentialIdentity, bool) {
	identity, err := parseCodexCredentialIdentity(data)
	return identity, err == nil
}

// localCredentialIdentifiesRecord validates the non-secret identity that can be
// derived from one AO-owned credential. Legacy OAuth credentials without an
// account id remain distinguishable by their private account slot and exact
// bytes at the reconciliation/switch boundaries.
func localCredentialIdentifiesRecord(record codexAccountRecord, data []byte) bool {
	identity, err := parseCodexCredentialIdentity(data)
	if err != nil {
		return false
	}
	if record.ProviderAccountID != "" {
		return identity.Method == domain.CodexAuthMethodChatGPT && identity.ProviderAccountID == record.ProviderAccountID
	}
	if record.Snapshot.AuthMethod != domain.CodexAuthMethodUnknown && record.Snapshot.AuthMethod != identity.Method {
		return false
	}
	// API keys and legacy OAuth credentials have no non-secret stable account
	// identifier. They belong to a saved slot only when their private bytes still
	// match exactly. Keep that comparison in memory and never expose either value.
	saved, savedErr := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	return savedErr == nil && bytes.Equal(saved, data)
}

func safeProviderAccountID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}
