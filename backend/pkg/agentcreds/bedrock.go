package agentcreds

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// bedrockRequest builds the Bedrock probe for either credential shape.
//
// It targets the control plane (bedrock.{region}.amazonaws.com), never
// bedrock-runtime: listing foundation models is free and side-effect free,
// while the runtime endpoint bills tokens. The response doubles as the
// Bedrock-format model catalog, which is the only correct source of Bedrock
// model IDs.
func (v *Validator) bedrockRequest(ctx context.Context, cred Credential) (requestSpec, error) {
	region := strings.TrimSpace(cred.Region)
	if region == "" {
		return requestSpec{}, fmt.Errorf("agentcreds: Bedrock needs a region")
	}
	base := firstNonEmpty(cred.BaseURL, v.endpoint.bedrock, fmt.Sprintf("https://bedrock.%s.amazonaws.com", region))
	endpoint := strings.TrimRight(base, "/") + "/foundation-models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}

	switch cred.Kind {
	case KindAuthToken, KindOAuthToken, KindAPIKey:
		// AWS_BEARER_TOKEN_BEDROCK: a pre-signed bearer, no signing needed.
		secret := strings.TrimSpace(cred.Secret)
		if secret == "" {
			return requestSpec{}, fmt.Errorf("agentcreds: no Bedrock bearer token from %s", cred.Source)
		}
		request.Header.Set("Authorization", "Bearer "+secret)
	case KindAWSSigV4:
		keys, err := parseAWSKeys(cred.Secret)
		if err != nil {
			return requestSpec{}, err
		}
		if err := signAWSRequestV4(request, keys, "bedrock", region, v.now().UTC()); err != nil {
			return requestSpec{}, err
		}
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Bedrock", cred.Kind)
	}

	return requestSpec{
		request: request, parseModels: parseBedrockModels,
		// Valid AWS credentials with no Bedrock entitlement return 200 and an
		// empty or non-Anthropic list. Status alone is not the verdict.
		requireModels: true, label: "AWS Bedrock",
	}, nil
}

// parseBedrockModels reads Bedrock's foundation-model summaries.
func parseBedrockModels(body []byte) ([]Model, error) {
	var payload struct {
		ModelSummaries []struct {
			ModelID       string `json:"modelId"`
			ProviderName  string `json:"providerName"`
			ModelLifecyle struct {
				Status string `json:"status"`
			} `json:"modelLifecycle"`
		} `json:"modelSummaries"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// Bedrock's control plane does not report reasoning levels, so Efforts stays
	// empty here. That is honest rather than lossy: an empty list means "this
	// provider did not say", and the picker shows no effort control instead of
	// inventing levels the account may not have.
	models := make([]Model, 0, len(payload.ModelSummaries))
	for _, summary := range payload.ModelSummaries {
		if isClaudeModelID(summary.ModelID) || strings.EqualFold(summary.ProviderName, "Anthropic") {
			models = append(models, Model{ID: summary.ModelID})
		}
	}
	return models, nil
}

// awsKeys is a static AWS credential triple.
type awsKeys struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// parseAWSKeys reads the packed "access\nsecret\nsession" form the resolver
// produces. Packing the triple into Credential.Secret keeps one secret field
// on the struct, so there is exactly one place that must never be logged.
func parseAWSKeys(secret string) (awsKeys, error) {
	parts := strings.Split(secret, "\n")
	if len(parts) < 2 {
		return awsKeys{}, fmt.Errorf("agentcreds: malformed AWS credential")
	}
	keys := awsKeys{
		AccessKeyID:     strings.TrimSpace(parts[0]),
		SecretAccessKey: strings.TrimSpace(parts[1]),
	}
	if len(parts) > 2 {
		keys.SessionToken = strings.TrimSpace(parts[2])
	}
	if keys.AccessKeyID == "" || keys.SecretAccessKey == "" {
		return awsKeys{}, fmt.Errorf("agentcreds: incomplete AWS credential")
	}
	return keys, nil
}

// signAWSRequestV4 signs a request with AWS Signature Version 4.
//
// This is ~100 lines of stdlib crypto instead of a dependency on
// aws-sdk-go-v2, and it is bounded: it covers static credentials for GET
// requests with no payload, which is all a model-listing probe needs. It
// deliberately does NOT reimplement the AWS credential chain — IMDS, container
// credentials, SSO profiles, role assumption, workload identity federation.
// Those are what the SDK actually is, and they are handled by delegating to
// the aws CLI, which has already resolved them.
//
// SigV4 in four steps: canonicalize the request, hash it into a string to
// sign, derive a signing key by chaining HMACs over date/region/service, then
// sign. See docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html
func signAWSRequestV4(request *http.Request, keys awsKeys, service, region string, now time.Time) error {
	const algorithm = "AWS4-HMAC-SHA256"
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	request.Header.Set("X-Amz-Date", amzDate)
	if keys.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", keys.SessionToken)
	}
	if request.Host != "" {
		request.Header.Set("Host", request.Host)
	} else {
		request.Header.Set("Host", request.URL.Host)
	}

	// An empty payload still has a hash, and it must match the one declared in
	// the signed headers or the signature is rejected.
	payloadHash := hex.EncodeToString(sha256.New().Sum(nil))
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders, canonicalHeaders := canonicalizeHeaders(request)
	canonicalRequest := strings.Join([]string{
		request.Method,
		canonicalURIPath(request.URL.EscapedPath()),
		request.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	hashedRequest := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		algorithm, amzDate, scope, hex.EncodeToString(hashedRequest[:]),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+keys.SecretAccessKey), dateStamp)
	signingKey = hmacSHA256(signingKey, region)
	signingKey = hmacSHA256(signingKey, service)
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	request.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, keys.AccessKeyID, scope, signedHeaders, signature,
	))
	return nil
}

// canonicalizeHeaders builds SigV4's canonical header block: lowercase names,
// sorted, with runs of whitespace in values collapsed.
func canonicalizeHeaders(request *http.Request) (signedHeaders, canonicalHeaders string) {
	names := make([]string, 0, len(request.Header))
	values := make(map[string]string, len(request.Header))
	for name := range request.Header {
		lowered := strings.ToLower(name)
		names = append(names, lowered)
		values[lowered] = strings.Join(strings.Fields(request.Header.Get(name)), " ")
	}
	if _, ok := values["host"]; !ok {
		names = append(names, "host")
		values["host"] = request.URL.Host
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(values[name])
		builder.WriteByte('\n')
	}
	return strings.Join(names, ";"), builder.String()
}

// canonicalURIPath normalizes the path component. An empty path signs as "/".
func canonicalURIPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
