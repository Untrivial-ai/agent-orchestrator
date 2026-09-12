package domain

import "time"

// ShareLink is a redeemable invitation to a project or session.
type ShareLink struct {
	ID              string
	OrgID           string
	ProjectID       string
	SessionID       string // empty means the whole project
	CreatedByUserID string
	Role            string // "viewer" or "editor"
	Status          string // "active" or "revoked"
	AccessScope     string // "anyone" or "restricted"
	Recipients      []string
	Interaction     string // "view" or "interact"
	ModeCap         string // "read-only", "standard", "trusted", or empty (no cap beyond the session's own mode)
	DeniedCommands  []string
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateShareLink is the input to mint a new ShareLink.
type CreateShareLink struct {
	SessionID      string
	Role           string
	AccessScope    string
	Recipients     []string
	Interaction    string
	ModeCap        string
	DeniedCommands []string
	ExpiresAt      *time.Time
}

type UpdateShareGrant struct {
	Role      string
	ModeCap   string
	SessionID string
}

// ShareGrant is a redeemed share and its frozen access policy.
type ShareGrant struct {
	ID              string
	OrgID           string
	ProjectID       string
	SessionID       string
	UserID          string
	UserEmail       string
	UserDisplayName string
	SharedByUserID  string
	Role            string
	Status          string
	ModeCap         string
	DeniedCommands  []string
	RedeemedAt      time.Time
	UpdatedAt       time.Time
}

// SharedProject is a project or session shared with the calling user.
type SharedProject struct {
	Grant         ShareGrant
	Project       Project
	SessionID     string
	SessionName   string
	SharedByEmail string
	SharedByName  string
}
