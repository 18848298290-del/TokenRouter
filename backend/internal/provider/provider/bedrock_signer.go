package provider

import (
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
)

// Bedrock SigV4 authentication signs against the runtime endpoint's source region.
// A global inference model can still use a regional HTTP endpoint, so signer
// construction resolves the provider's runtime region separately from the model
// target and carries an optional AWS session token.

// NewBedrockSignerFromProvider builds a SigV4 signer from stored AWS credentials.
// A nonnil record with both access-key and secret-key values is required. Missing
// values return errors; the runtime region resolver supplies the default region
// when aws_region is empty, and aws_session_token is optional.
func NewBedrockSignerFromProvider(value *provider.Record) (*bedrock.BedrockSigner, error) {
	accessKeyID := value.GetCredential("aws_access_key_id")
	if accessKeyID == "" {
		return nil, fmt.Errorf("aws_access_key_id not found in credentials")
	}
	secretAccessKey := value.GetCredential("aws_secret_access_key")
	if secretAccessKey == "" {
		return nil, fmt.Errorf("aws_secret_access_key not found in credentials")
	}
	// Global inference IDs describe the model reach, while SigV4 signs the regional
	// runtime endpoint selected from aws_region; using the inference scope
	// here would
	// sign requests for a different endpoint region.
	region := bedrock.BedrockRuntimeRegion(&bedrock.RouteInput{Region: value.GetCredential("aws_region"), ForceGlobal: value.GetCredential("aws_force_global") == "true"})
	sessionToken := value.GetCredential("aws_session_token") // optional temporary-credential token

	return bedrock.NewBedrockSigner(accessKeyID, secretAccessKey, sessionToken, region), nil
}
