package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	workOSAPIBaseURL      = "https://api.workos.com"
	maxWorkOSCacheEntries = 1024
)

type cachedProfile struct {
	email       string
	displayName string
	expiresAt   time.Time
}

type cachedOrganization struct {
	displayName string
	expiresAt   time.Time
}

func NewWorkOSProfileResolver(apiKey string, client *http.Client) (ProfileResolver, error) {
	return newWorkOSProfileResolver(apiKey, workOSAPIBaseURL, client)
}

func newWorkOSProfileResolver(
	apiKey string,
	baseURL string,
	client *http.Client,
) (ProfileResolver, error) {
	apiKey = strings.TrimSpace(apiKey)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if apiKey == "" {
		return nil, errors.New("WorkOS API key is required")
	}
	if baseURL == "" {
		return nil, errors.New("WorkOS API base URL is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var mutex sync.Mutex
	cache := make(map[string]cachedProfile)

	return func(ctx context.Context, userID string) (string, string, error) {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			return "", "", errors.New("WorkOS user ID is required")
		}
		now := time.Now()
		mutex.Lock()
		cached, ok := cache[userID]
		mutex.Unlock()
		if ok && now.Before(cached.expiresAt) {
			return cached.email, cached.displayName, nil
		}

		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/user_management/users/"+url.PathEscape(userID),
			http.NoBody,
		)
		if err != nil {
			return "", "", err
		}
		request.Header.Set("Authorization", "Bearer "+apiKey)
		response, err := client.Do(request)
		if err != nil {
			return "", "", fmt.Errorf("get WorkOS user: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("get WorkOS user: status %d", response.StatusCode)
		}
		var user struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&user); err != nil {
			return "", "", err
		}
		if strings.TrimSpace(user.ID) != userID {
			return "", "", errors.New("WorkOS user response did not match token")
		}
		email := strings.ToLower(strings.TrimSpace(user.Email))
		if email == "" {
			return "", "", errors.New("WorkOS user has no email")
		}
		displayName := strings.TrimSpace(strings.Join(
			nonEmpty(user.FirstName, user.LastName),
			" ",
		))
		if displayName == "" {
			displayName = email
		}
		mutex.Lock()
		trimCache(cache, func(profile cachedProfile) bool {
			return !now.Before(profile.expiresAt)
		})
		cache[userID] = cachedProfile{
			email:       email,
			displayName: displayName,
			expiresAt:   now.Add(5 * time.Minute),
		}
		mutex.Unlock()
		return email, displayName, nil
	}, nil
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func NewWorkOSOrganizationResolver(
	apiKey string,
	client *http.Client,
) (OrganizationResolver, error) {
	return newWorkOSOrganizationResolver(apiKey, workOSAPIBaseURL, client)
}

func newWorkOSOrganizationResolver(
	apiKey string,
	baseURL string,
	client *http.Client,
) (OrganizationResolver, error) {
	apiKey = strings.TrimSpace(apiKey)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if apiKey == "" || baseURL == "" {
		return nil, errors.New("WorkOS API key and base URL are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var mutex sync.Mutex
	cache := make(map[string]cachedOrganization)

	return func(ctx context.Context, organizationID string) (string, error) {
		organizationID = strings.TrimSpace(organizationID)
		if organizationID == "" {
			return "", errors.New("WorkOS organization ID is required")
		}
		now := time.Now()
		mutex.Lock()
		cached, ok := cache[organizationID]
		mutex.Unlock()
		if ok && now.Before(cached.expiresAt) {
			return cached.displayName, nil
		}

		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/organizations/"+url.PathEscape(organizationID),
			http.NoBody,
		)
		if err != nil {
			return "", err
		}
		request.Header.Set("Authorization", "Bearer "+apiKey)
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("get WorkOS organization: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("get WorkOS organization: status %d", response.StatusCode)
		}
		var organization struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&organization); err != nil {
			return "", err
		}
		if strings.TrimSpace(organization.ID) != organizationID {
			return "", errors.New("WorkOS organization response did not match token")
		}
		displayName := strings.TrimSpace(organization.Name)
		if displayName == "" {
			displayName = "WorkOS organization"
		}
		mutex.Lock()
		trimCache(cache, func(organization cachedOrganization) bool {
			return !now.Before(organization.expiresAt)
		})
		cache[organizationID] = cachedOrganization{
			displayName: displayName,
			expiresAt:   now.Add(5 * time.Minute),
		}
		mutex.Unlock()
		return displayName, nil
	}, nil
}

func trimCache[T any](cache map[string]T, expired func(T) bool) {
	if len(cache) < maxWorkOSCacheEntries {
		return
	}
	for key, value := range cache {
		if expired(value) {
			delete(cache, key)
		}
	}
	for len(cache) >= maxWorkOSCacheEntries {
		for key := range cache {
			delete(cache, key)
			break
		}
	}
}
