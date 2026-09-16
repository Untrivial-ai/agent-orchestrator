package agentcreds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

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
	base := firstNonEmpty(cred.BaseURL, fmt.Sprintf("https://bedrock.%s.amazonaws.com", region))
	endpoint := strings.TrimRight(base, "/") + "/foundation-models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return requestSpec{}, err
	}

	switch cred.Kind {
	case KindAuthToken:
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
		if err := awsv4.NewSigner().SignHTTP(
			ctx,
			keys,
			request,
			emptyPayloadSHA256,
			"bedrock",
			region,
			time.Now().UTC(),
		); err != nil {
			return requestSpec{}, fmt.Errorf("agentcreds: sign Bedrock request: %w", err)
		}
	default:
		return requestSpec{}, fmt.Errorf("agentcreds: credential kind %q cannot authenticate to Bedrock", cred.Kind)
	}

	return requestSpec{
		request: request, parseModels: parseBedrockModels,
		// Valid AWS credentials with no Bedrock entitlement return 200 and an
		// empty or non-Anthropic list. Status alone is not the verdict.
		requireModels: true, catalogOnly: true, label: "AWS Bedrock",
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

// parseAWSKeys reads the packed "access\nsecret\nsession" form the resolver
// produces. Packing the triple into Credential.Secret keeps one secret field
// on the struct, so there is exactly one place that must never be logged.
func parseAWSKeys(secret string) (aws.Credentials, error) {
	parts := strings.Split(secret, "\n")
	if len(parts) < 2 {
		return aws.Credentials{}, fmt.Errorf("agentcreds: malformed AWS credential")
	}
	keys := aws.Credentials{
		AccessKeyID:     strings.TrimSpace(parts[0]),
		SecretAccessKey: strings.TrimSpace(parts[1]),
	}
	if len(parts) > 2 {
		keys.SessionToken = strings.TrimSpace(parts[2])
	}
	if keys.AccessKeyID == "" || keys.SecretAccessKey == "" {
		return aws.Credentials{}, fmt.Errorf("agentcreds: incomplete AWS credential")
	}
	return keys, nil
}
