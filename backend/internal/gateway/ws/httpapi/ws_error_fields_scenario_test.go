package httpapi

import (
	"testing"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// WS classification, log summaries and HTTP status mapping can reuse one parse
// of the same error frame. The benchmark passes those extracted values to each
// helper so measured work includes their shared-field reuse, with results
// retained in package sinks.

// These sinks retain benchmark classification and diagnostic results so repeated
// helper work remains observable after each measured iteration completes.
var (
	benchmarkOpenAIWSStringSink string // retained diagnostic string result
	benchmarkOpenAIWSBoolSink   bool   // retained classification or status result
)

// BenchmarkOpenAIWSErrorEventFieldReuse parses one invalid-request frame and
// reuses its fields for classification, logging and status mapping. Package
// sinks retain each result while allocation reporting measures repeated
// combined helper work after fixture preparation.
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
