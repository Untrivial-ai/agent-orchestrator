// Package domain contains the hosted control plane's private domain types.
package domain

// Principal is the AO identity resolved from a verified external identity or
// an AO-issued access token. Organization membership is deliberately absent:
// callers must load current memberships from PostgreSQL for every request.
type Principal struct {
	UserID      string
	Provider    string
	ExternalID  string
	Email       string
	DisplayName string
}

// Membership describes one active organization membership for a principal.
type Membership struct {
	OrgID       string
	OrgSlug     string
	DisplayName string
	Role        string
}
