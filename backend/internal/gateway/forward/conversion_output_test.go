package forward

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAppendRawJSON_EmptyObjectPlaceholder supplies a whitespace-formatted empty
// object and a nonempty object as existing argument bytes. The placeholder is
// replaced, while the nonempty object's literal bytes precede the new fragment.
func TestAppendRawJSON_EmptyObjectPlaceholder(t *testing.T) {
	t.Parallel()

	fragment := `{"query":"status"}`
	require.JSONEq(t, fragment, string(AppendRawJSON(json.RawMessage("{ \n\t }"), fragment)))
	require.Equal(t, `{"existing":true}{"query":"status"}`, string(AppendRawJSON(json.RawMessage(`{"existing":true}`), fragment)))
}
