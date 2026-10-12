package upstream

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

// Unpadded base64 must decode to the original PNG bytes and MIME type.
func TestImageByteDefaultsRemainCompatible(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\nfixture")
	decoded, err := DecodeBase64Image(base64.RawStdEncoding.EncodeToString(data))
	require.NoError(t, err)
	require.Equal(t, data, decoded.Bytes)
	require.Equal(t, "image/png", decoded.Mime)
}
