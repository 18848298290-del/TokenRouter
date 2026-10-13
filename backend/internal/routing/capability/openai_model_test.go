package capability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Recorded max remains max across the supplied model families and
// qualifications. The inputs model an effort already sent upstream; the test
// checks reporting consistency, while supported request values belong to the
// separate capability resolver.

// TestNormalizeOpenAIReasoningEffortForGPT56 records max for qualified, dated,
// and cross-family model names. Each supplied case must keep max because this
// recording helper ignores model capability inference.
func TestNormalizeOpenAIReasoningEffortForGPT56(t *testing.T) {
	tests := []struct {
		name  string // subtest case display name
		raw   string // recorded upstream effort value
		model string // model ignored during recording
		want  string // expected normalized recorded value
	}{
		{name: "Sol 保留 max", raw: "max", model: "gpt-5.6-sol", want: "max"},
		{name: "供应商限定型号保留显式 max", raw: "max", model: "openai/gpt-5.6-terra", want: "max"},
		{name: "Luna 后缀保留 max", raw: "max", model: "gpt-5.6-luna-2026-07-09", want: "max"},
		{name: "DeepSeek V4 保留 max", raw: "max", model: "deepseek-v4-pro", want: "max"},
		{name: "GLM 保留 max", raw: "max", model: "glm-5.2", want: "max"},
		{name: "记录已发送档位不推断模型能力", raw: "max", model: "gpt-5.5", want: "max"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeRecordedOpenAIEffortForModel(tt.raw, tt.model))
		})
	}
}
