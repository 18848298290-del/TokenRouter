package httpapi

// Error-field scenarios measure parsing and reuse by WS error classification and diagnostics.

import (
	"testing"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

var (
	benchmarkOpenAIWSStringSink string
	benchmarkOpenAIWSBoolSink   bool
)

func BenchmarkOpenAIWSErrorEventFieldReuse(b *testing.B) {
	event := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		codeRaw, errTypeRaw, errMsgRaw := protocolopenai.ParseWSErrorEventFields(event)
		benchmarkOpenAIWSStringSink, benchmarkOpenAIWSBoolSink = openai.ClassifyWSErrorEventFromRaw(codeRaw, errTypeRaw, errMsgRaw)
		code, errType, errMsg := gatewayprovider.SummarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMsgRaw)
		benchmarkOpenAIWSStringSink = code
		benchmarkOpenAIWSStringSink = errType
		benchmarkOpenAIWSStringSink = errMsg
		benchmarkOpenAIWSBoolSink = openai.WSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw) > 0
	}
}
