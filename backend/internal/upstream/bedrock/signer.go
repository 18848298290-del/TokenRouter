package bedrock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// BedrockSigner signs requests for the Bedrock service using AWS SigV4.
type BedrockSigner struct {
	credentials aws.Credentials
	Region      string
	signer      *v4.Signer
}

// NewBedrockSigner configures credentials and the AWS signing region.
func NewBedrockSigner(accessKeyID, secretAccessKey, sessionToken, region string) *BedrockSigner {
	return &BedrockSigner{
		credentials: aws.Credentials{
			AccessKeyID:     accessKeyID,
			SecretAccessKey: secretAccessKey,
			SessionToken:    sessionToken,
		},
		Region: region,
		signer: v4.NewSigner(),
	}
}

// SignRequest signs req using the hash of body and the current time.
// Callers must pass the bytes that will be sent as the request body.
// BuildRequest sets Content-Type and Accept before signing.
// Signed headers and payload must remain consistent when the request is sent.
func (s *BedrockSigner) SignRequest(ctx context.Context, req *http.Request, body []byte) error {
	payloadHash := SHA256Hash(body)
	return s.signer.SignHTTP(ctx, s.credentials, req, payloadHash, "bedrock", s.Region, time.Now())
}

func SHA256Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
