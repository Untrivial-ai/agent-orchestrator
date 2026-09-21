package domain

import "time"

// OrgMember is one active organization membership joined with the member's
// user profile — everything a members list needs in one row.
type OrgMember struct {
	UserID      string
	Email       string
	DisplayName string
	Role        string
	CreatedAt   time.Time
}

// Invitation is a pending, accepted, declined, revoked, or expired invitation
// to join an organization. It persists into the ao_org_invitations table
// (see migration 00001), which predates this feature.
type Invitation struct {
	ID              string
	OrgID           string
	Email           string
	InvitedByUserID string
	InvitedByEmail  string
	InvitedByName   string
	Role            string
	Status          string
	ExpiresAt       time.Time
	AcceptedAt      *time.Time
	DeclinedAt      *time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateInvitation is the input to invite a new member into an organization.
type CreateInvitation struct {
	Email string
	Role  string
}
