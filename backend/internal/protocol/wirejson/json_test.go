package wirejson

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDecodeOpenAIJSONUseNumberRejectsTrailingDocument concatenates two valid
// JSON objects into one request body. Decoding must reject the extra document
// so a repair cannot silently retain only the first object.
func TestDecodeOpenAIJSONUseNumberRejectsTrailingDocument(t *testing.T) {
	var decoded map[string]any
	require.Error(t, DecodeUseNumber([]byte(`{"name":"python"}{"extra":true}`), &decoded))
}
