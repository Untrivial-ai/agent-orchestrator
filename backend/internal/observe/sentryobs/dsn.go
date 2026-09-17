package sentryobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const maxAgentSwitchDSNBytes = 4 << 10

var (
	agentSwitchPublicKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	agentSwitchProjectPattern   = regexp.MustCompile(`^\d{1,20}$`)
)

// AgentSwitchDestination is a normalized, non-secret Sentry envelope target.
type AgentSwitchDestination struct {
	Endpoint    *url.URL
	PublicKey   string
	ProjectID   string
	Fingerprint string
}

// ParseAgentSwitchDSN validates a standard public-key Sentry DSN. Production
// destinations require HTTPS; development HTTP destinations must be loopback.
func ParseAgentSwitchDSN(raw string, production bool) (AgentSwitchDestination, error) {
	if raw == "" || len(raw) > maxAgentSwitchDSNBytes || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") {
		return AgentSwitchDestination{}, errors.New("invalid Sentry DSN")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" {
		return AgentSwitchDestination{}, errors.New("invalid Sentry DSN")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return AgentSwitchDestination{}, errors.New("sentry DSN cannot contain query or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return AgentSwitchDestination{}, errors.New("sentry DSN scheme must be HTTP or HTTPS")
	}
	if production && scheme != "https" {
		return AgentSwitchDestination{}, errors.New("production Sentry DSN must use HTTPS")
	}
	if parsed.User == nil {
		return AgentSwitchDestination{}, errors.New("sentry DSN public key is required")
	}
	if _, hasSecret := parsed.User.Password(); hasSecret {
		return AgentSwitchDestination{}, errors.New("sentry DSN secret is not supported")
	}
	publicKey := parsed.User.Username()
	if !agentSwitchPublicKeyPattern.MatchString(publicKey) {
		return AgentSwitchDestination{}, errors.New("invalid Sentry DSN public key")
	}
	host, err := normalizeAgentSwitchHost(parsed.Hostname())
	if err != nil {
		return AgentSwitchDestination{}, err
	}
	if scheme == "http" && !agentSwitchLoopbackHost(host) {
		return AgentSwitchDestination{}, errors.New("development HTTP Sentry DSN must use loopback")
	}
	defaultPort := "443"
	if scheme == "http" {
		defaultPort = "80"
	}
	port := parsed.Port()
	if port == "" {
		port = defaultPort
	} else {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			return AgentSwitchDestination{}, errors.New("invalid Sentry DSN port")
		}
		port = strconv.Itoa(portNumber)
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return AgentSwitchDestination{}, errors.New("invalid Sentry DSN port")
	}
	basePath, projectID, err := normalizeAgentSwitchDSNPath(parsed)
	if err != nil {
		return AgentSwitchDestination{}, err
	}
	endpointHost := host
	if net.ParseIP(host) != nil {
		endpointHost = "[" + host + "]"
	}
	if port != defaultPort {
		endpointHost = net.JoinHostPort(host, port)
	}
	endpointPath := basePath + "/api/" + projectID + "/envelope/"
	if basePath == "" {
		endpointPath = "/api/" + projectID + "/envelope/"
	}
	endpoint := &url.URL{Scheme: scheme, Host: endpointHost, Path: endpointPath}
	material := strings.Join([]string{scheme, host, port, basePath, projectID, publicKey}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return AgentSwitchDestination{Endpoint: endpoint, PublicKey: publicKey, ProjectID: projectID, Fingerprint: hex.EncodeToString(sum[:])}, nil
}

func normalizeAgentSwitchHost(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("sentry DSN host is required")
	}
	host := strings.ToLower(raw)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return "", errors.New("invalid Sentry DSN host")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid Sentry DSN host")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("invalid Sentry DSN host")
			}
		}
	}
	return host, nil
}

func agentSwitchLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeAgentSwitchDSNPath(parsed *url.URL) (string, string, error) {
	escaped := parsed.EscapedPath()
	if escaped == "" || escaped[0] != '/' || strings.HasSuffix(escaped, "/") {
		return "", "", errors.New("sentry DSN numeric project ID is required")
	}
	segments := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			return "", "", errors.New("invalid Sentry DSN base path")
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") {
			return "", "", errors.New("invalid Sentry DSN path escaping")
		}
		normalized = append(normalized, decoded)
	}
	projectRaw := normalized[len(normalized)-1]
	if !agentSwitchProjectPattern.MatchString(projectRaw) {
		return "", "", errors.New("sentry DSN project ID must be numeric")
	}
	projectNumber, err := strconv.ParseUint(projectRaw, 10, 64)
	if err != nil || projectNumber == 0 {
		return "", "", errors.New("invalid Sentry DSN project ID")
	}
	projectID := strconv.FormatUint(projectNumber, 10)
	baseSegments := normalized[:len(normalized)-1]
	basePath := ""
	if len(baseSegments) > 0 {
		basePath = "/" + strings.Join(baseSegments, "/")
	}
	return basePath, projectID, nil
}

func (destination AgentSwitchDestination) validate() error {
	if destination.Endpoint == nil || destination.Endpoint.Scheme == "" || destination.Endpoint.Host == "" || destination.PublicKey == "" || destination.ProjectID == "" || destination.Fingerprint == "" {
		return fmt.Errorf("incomplete agent switch Sentry destination")
	}
	return nil
}
