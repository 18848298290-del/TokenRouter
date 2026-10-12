package tierpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDecisionPreservesLazyPolicyOrder checks short-circuiting, group force_off, and lazy price checks.
func TestDecisionPreservesLazyPolicyOrder(t *testing.T) {
	for _, tc := range []struct {
		name, group, key, original, action string
		want                               []string
	}{
		{name: "系统拒绝", original: "priority", action: "block", want: []string{"system:priority"}},
		{name: "分组关闭", group: "force_off", key: "force_on", want: nil},
		{name: "Key开启后复核系统", key: "force_on", want: []string{"key", "price", "system:priority"}},
		{name: "Key关闭不查价", key: "force_off", original: "priority", want: []string{"system:priority", "key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			input := DecisionInput{
				Model: "gpt-5.5", GroupPolicy: tc.group, OpenAI: true,
				Evaluate: func(tier string) (string, string) {
					calls = append(calls, "system:"+tier)
					return tc.action, ""
				},
				KeyPolicy:        func() string { calls = append(calls, "key"); return tc.key },
				ForceOnSupported: func() bool { calls = append(calls, "price"); return true },
			}
			Resolve(input, tc.original, tc.original != "")
			require.Equal(t, tc.want, calls)
		})
	}
}

// TestEmptyBodyDoesNotReadFastPolicy checks that empty input bypasses policy reads.
func TestEmptyBodyDoesNotReadFastPolicy(t *testing.T) {
	result, err := ApplyBody(nil, DecisionInput{})
	require.NoError(t, err)
	require.Nil(t, result)
}
