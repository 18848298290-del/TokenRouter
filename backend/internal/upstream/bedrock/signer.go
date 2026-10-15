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

// signer.go signs prepared Bedrock requests with AWS credentials and a payload
// digest.
// AWS SigV4 signs the same payload bytes and request headers that Bedrock
// receives. The signer keeps caller-supplied credentials and source region,
// while the AWS SDK mutates the prepared request in place using a SHA-256
// payload digest and current signing time.

// BedrockSigner supplies retained AWS credentials and source region to the
// SigV4 library.
type BedrockSigner struct {
	credentials aws.Credentials // retained AWS signing credentials
	// Region is read during every signature and must match the request endpoint
	// source region. Callers finish configuration before using the signer
	// concurrently.
	Region string     // current signing source region
	signer *v4.Signer // initialized SDK signing implementation
}

// NewBedrockSigner retains the supplied credential strings and region and
// creates an SDK signer. Callers validate those credentials and resolve region
// defaults before construction; this constructor keeps the supplied region
// spelling.
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

// SignRequest mutates req with the Bedrock SigV4 signature using body bytes
// and current time. Callers supply an initialized signer, finish headers
// before signing, and send the same body and signed-header values afterward.
func (s *BedrockSigner) SignRequest(ctx context.Context, req *http.Request, body []byte) error {
	payloadHash := SHA256Hash(body)
	return s.signer.SignHTTP(ctx, s.credentials, req, payloadHash, "bedrock", s.Region, time.Now())
}

// SHA256Hash returns the lowercase hexadecimal SHA-256 digest of the supplied
// bytes.
func SHA256Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
