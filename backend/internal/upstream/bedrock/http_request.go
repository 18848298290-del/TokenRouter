package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
)

// Bedrock Invoke URLs choose streaming through their action path and carry the
// routed model in that URL. Body preparation occurs upstream of these
// builders, while bearer authentication or SigV4 is applied after JSON content
// and accept headers are set.

// BuildRequest creates a regional Invoke request and signs its prepared body
// with the supplied initialized signer. Callers pass the resolved model/region
// and leave borrowed body bytes and signed headers consistent until the
// transport sends the request.
func BuildRequest(
	ctx context.Context,
	body []byte,
	modelID string,
	region string,
	stream bool,
	signer *BedrockSigner,
) (*http.Request, error) {
	targetURL := BuildBedrockURL(region, modelID, stream)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if err := signer.SignRequest(ctx, req, body); err != nil {
		return nil, fmt.Errorf("sign bedrock request: %w", err)
	}

	return req, nil
}

// BuildRequestAPIKey creates the regional Invoke request with bearer
// authentication and prepared body bytes. Callers validate the API key and
// routed model, align stream framing, and retain body bytes unchanged until
// the request is sent.
func BuildRequestAPIKey(
	ctx context.Context,
	body []byte,
	modelID string,
	region string,
	stream bool,
	apiKey string,
) (*http.Request, error) {
	targetURL := BuildBedrockURL(region, modelID, stream)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	return req, nil
}
