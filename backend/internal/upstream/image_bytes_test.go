package upstream

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageByteDefaultsRemainCompatible supplies an unpadded standard-base64
// payload with a PNG prefix. It checks the decoded fixture bytes and PNG label
// returned to image consumers.
func TestImageByteDefaultsRemainCompatible(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\nfixture")
	decoded, err := DecodeBase64Image(base64.RawStdEncoding.EncodeToString(data))
	require.NoError(t, err)
	require.Equal(t, data, decoded.Bytes)
	require.Equal(t, "image/png", decoded.Mime)
}
