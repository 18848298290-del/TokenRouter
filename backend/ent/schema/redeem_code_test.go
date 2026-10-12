package schema

import (
	"strings"
	"testing"

	"entgo.io/ent/schema/field"
	"github.com/stretchr/testify/require"
)

// TestRedeemCode_CodeLength checks code-point limits and rejects empty codes.
func TestRedeemCode_CodeLength(t *testing.T) {
	var codeField *field.Descriptor
	for _, schemaField := range (RedeemCode{}).Fields() {
		if schemaField.Descriptor().Name == "code" {
			codeField = schemaField.Descriptor()
			break
		}
	}
	require.NotNil(t, codeField)
	require.Equal(t, 32, codeField.Size)
	require.NotEmpty(t, codeField.Validators)

	tests := []struct {
		name    string
		code    string
		invalid bool
	}{
		{name: "空兑换码", invalid: true},
		{name: "中文达到上限", code: strings.Repeat("兑", 32)},
		{name: "英文达到上限", code: strings.Repeat("a", 32)},
		{name: "中英混合达到上限", code: strings.Repeat("兑a", 16)},
		{name: "补充平面汉字达到上限", code: strings.Repeat("𠮷", 32)},
		{name: "中文超过上限", code: strings.Repeat("兑", 33), invalid: true},
		{name: "英文超过上限", code: strings.Repeat("a", 33), invalid: true},
		{name: "补充平面汉字超过上限", code: strings.Repeat("𠮷", 33), invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var validationErr error
			for _, validator := range codeField.Validators {
				validate, ok := validator.(func(string) error)
				require.True(t, ok)
				if validationErr = validate(tt.code); validationErr != nil {
					break
				}
			}
			if tt.invalid {
				require.Error(t, validationErr)
				return
			}
			require.NoError(t, validationErr)
		})
	}
}
