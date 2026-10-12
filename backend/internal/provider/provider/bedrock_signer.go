package provider

import (
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
)

// NewBedrockSignerFromProvider creates a signer from the provider credentials.
func NewBedrockSignerFromProvider(value *provider.Record) (*bedrock.BedrockSigner, error) {
	accessKeyID := value.GetCredential("aws_access_key_id")
	if accessKeyID == "" {
		return nil, fmt.Errorf("aws_access_key_id not found in credentials")
	}
	secretAccessKey := value.GetCredential("aws_secret_access_key")
	if secretAccessKey == "" {
		return nil, fmt.Errorf("aws_secret_access_key not found in credentials")
	}
	// Use the HTTP endpoint source region for signing even when inference uses a global model.
	region := bedrock.BedrockRuntimeRegion(&bedrock.RouteInput{Region: value.GetCredential("aws_region"), ForceGlobal: value.GetCredential("aws_force_global") == "true"})
	sessionToken := value.GetCredential("aws_session_token") // Optional.

	return bedrock.NewBedrockSigner(accessKeyID, secretAccessKey, sessionToken, region), nil
}
