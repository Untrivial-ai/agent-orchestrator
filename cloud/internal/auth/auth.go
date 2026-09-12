package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidToken        = errors.New("invalid access token")
	ErrProviderUnavailable = errors.New("authentication provider unavailable")
)

var dummyPasswordHash, _ = bcrypt.GenerateFromPassword(
	[]byte("ao-cloud-invalid-password"),
	bcrypt.DefaultCost,
)

type WorkOSVerifier interface {
	Verify(ctx context.Context, token string) (domain.Principal, error)
}

type ProfileResolver func(
	ctx context.Context,
	userID string,
) (email string, displayName string, err error)

type OrganizationResolver func(
	ctx context.Context,
	organizationID string,
) (displayName string, err error)

type OIDCVerifier struct {
	verifier      *oidc.IDTokenVerifier
	clientID      string
	profiles      ProfileResolver
	organizations OrganizationResolver
}

func NewOIDCVerifier(
	ctx context.Context,
	issuer string,
	clientID string,
	jwksURL string,
	profiles ProfileResolver,
	organizations OrganizationResolver,
) (*OIDCVerifier, error) {
	if strings.TrimSpace(issuer) == "" ||
		strings.TrimSpace(clientID) == "" ||
		strings.TrimSpace(jwksURL) == "" {
		return nil, errors.New("WorkOS issuer, client ID, and JWKS URL are required")
	}
	return &OIDCVerifier{
		verifier: oidc.NewVerifier(
			strings.TrimSpace(issuer),
			oidc.NewRemoteKeySet(ctx, jwksURL),
			&oidc.Config{SkipClientIDCheck: true},
		),
		clientID:      strings.TrimSpace(clientID),
		profiles:      profiles,
		organizations: organizations,
	}, nil
}

func (v *OIDCVerifier) Verify(ctx context.Context, token string) (domain.Principal, error) {
	idToken, err := v.verifier.Verify(ctx, token)
	if err != nil {
		return domain.Principal{}, ErrInvalidToken
	}
	var claims struct {
		Subject    string `json:"sub"`
		Email      string `json:"email"`
		Name       string `json:"name"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
		ClientID   string `json:"client_id"`
		OrgID      string `json:"org_id"`
		Role       string `json:"role"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return domain.Principal{}, ErrInvalidToken
	}
	if strings.TrimSpace(claims.Subject) == "" ||
		strings.TrimSpace(claims.ClientID) != v.clientID {
		return domain.Principal{}, ErrInvalidToken
	}
	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(strings.Join(
			[]string{claims.GivenName, claims.FamilyName},
			" ",
		))
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if v.profiles != nil {
		resolvedEmail, resolvedName, err := v.profiles(ctx, claims.Subject)
		if err != nil {
			return domain.Principal{}, fmt.Errorf("%w: resolve WorkOS user: %v", ErrProviderUnavailable, err)
		}
		if resolvedEmail != "" {
			email = strings.ToLower(strings.TrimSpace(resolvedEmail))
		}
		if resolvedName != "" {
			displayName = strings.TrimSpace(resolvedName)
		}
	}
	if email == "" {
		return domain.Principal{}, ErrInvalidToken
	}
	if displayName == "" {
		displayName = email
	}
	orgID := strings.TrimSpace(claims.OrgID)
	orgName := ""
	if orgID != "" && v.organizations != nil {
		orgName, err = v.organizations(ctx, orgID)
		if err != nil {
			return domain.Principal{}, fmt.Errorf("%w: resolve WorkOS organization: %v", ErrProviderUnavailable, err)
		}
	}
	return domain.Principal{
		Provider:      "workos",
		ExternalID:    claims.Subject,
		Email:         email,
		DisplayName:   displayName,
		ExternalOrgID: orgID,
		OrgName:       strings.TrimSpace(orgName),
		OrgRole:       normalizeOrganizationRole(claims.Role),
	}, nil
}

func normalizeOrganizationRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return "owner"
	case "admin":
		return "admin"
	default:
		return "member"
	}
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = string(dummyPasswordHash)
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func NewOpaqueToken() (string, []byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	token := "ao_local_" + base64.RawURLEncoding.EncodeToString(value)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
