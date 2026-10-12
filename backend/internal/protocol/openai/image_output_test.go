package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIImageOutputCounter_TextOnlyResponsesStream(t *testing.T) {
	// A message output item in a text-only Responses stream must not count as an image.
	sseBody := `data: {"type":"response.created","response":{"id":"resp_123"}}

data: {"type":"response.output_item.added","item":{"id":"item_1","type":"message","role":"assistant","status":"in_progress"}}

data: {"type":"response.output_text.delta","item_id":"item_1","output_index":0,"content_index":0,"delta":"Hello"}

data: {"type":"response.output_item.done","item":{"id":"item_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}}

data: {"type":"response.completed","response":{"id":"resp_123","output":[{"id":"item_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}

data: [DONE]`

	if count := CountOpenAIImageOutputsFromSSEBody(sseBody); count != 0 {
		t.Fatalf("expected 0 images for text-only stream, got %d", count)
	}
}

func TestOpenAIImageOutputCounter_DataArraySkipsNonImageObjects(t *testing.T) {
	// Only data entries containing image output contribute to the image count.
	nonImageData := `data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"item_1","type":"message","content":[{"type":"output_text","text":"Hello"}]}]},"data":[{"id":"not_an_image","status":"done"}]}

data: [DONE]`

	if count := CountOpenAIImageOutputsFromSSEBody(nonImageData); count != 0 {
		t.Fatalf("expected 0 images for non-image data array, got %d", count)
	}

	imageData := `data: {"type":"response.completed","response":{"id":"resp_1","output":[]},"data":[{"url":"https://example.com/img.png","size":"1024x1024"}]}

data: [DONE]`

	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEBody(imageData)
	if count := counter.Count(); count != 1 {
		t.Fatalf("expected 1 image for image data array, got %d", count)
	}
	sizes := counter.Sizes()
	if len(sizes) != 1 || sizes[0] != "1024x1024" {
		t.Fatalf("expected image size from data array, got %#v", sizes)
	}
}

func TestOpenAIImageOutputCounter_CompletedImageEventRequiresOutput(t *testing.T) {
	// An image_generation.completed ID alone is insufficient: result, b64_json, or url is required.
	emptyCompleted := `data: {"type":"image_generation.completed","item":{"type":"image_generation.completed","id":"call_1"}}

data: [DONE]`

	if count := CountOpenAIImageOutputsFromSSEBody(emptyCompleted); count != 0 {
		t.Fatalf("expected 0 images for empty completed image event, got %d", count)
	}

	completedWithURL := `data: {"type":"image_generation.completed","item":{"type":"image_generation.completed","id":"call_1","url":"https://example.com/img.png"}}

data: [DONE]`

	if count := CountOpenAIImageOutputsFromSSEBody(completedWithURL); count != 1 {
		t.Fatalf("expected 1 image for completed image event with URL, got %d", count)
	}
}

func TestOpenAIImageOutputCounter_JSONDataArraySkipsNonImageObjects(t *testing.T) {
	// Non-image entries in a JSON data array must not trigger image billing.
	body := []byte(`{
		"id": "resp_1",
		"object": "response",
		"output": [
			{
				"id": "item_1",
				"type": "message",
				"content": [{"type": "output_text", "text": "Hello"}]
			}
		],
		"data": [{"id": "not_an_image", "status": "done"}],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	if count := CountOpenAIResponseImageOutputsFromJSONBytes(body); count != 0 {
		t.Fatalf("expected 0 images for JSON response with non-image data array, got %d", count)
	}
}

func TestOpenAIImageOutputCounterDeduplicatesFinalImages(t *testing.T) {
	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"abc"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"1024x1024"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"final-a"},{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"3840x2160"}]}}`))
	require.Equal(t, 2, counter.Count())
	require.Equal(t, []string{"1024x1024", "3840x2160"}, counter.Sizes())
}

func TestOpenAIImageOutputCounterCountsImagesAPIStreamShapes(t *testing.T) {
	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"image_generation.completed","id":"ig_complete","b64_json":"final-a"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_item","type":"image_generation_call","result":"final-b"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_done","type":"image_generation_call","result":"final-c"}]}}`))
	require.Equal(t, 3, counter.Count())

	dataCounter := NewOpenAIImageOutputCounter()
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"}]}`))
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"},{"b64_json":"c"}]}`))
	require.Equal(t, 3, dataCounter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEDataPayload(t *testing.T) {
	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEData([]byte("{\"type\":\"image_generation.completed\",\n\"b64_json\":\"final-a\"}"))
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEBodyPayload(t *testing.T) {
	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\n" +
			"data: \"b64_json\":\"final-a\"}\n\n" +
			"data: [DONE]\n\n",
	)
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterFallsBackForInvalidMultilineSSEBody(t *testing.T) {
	counter := NewOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-a\"}\n" +
			"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-b\"}\n\n",
	)
	require.Equal(t, 2, counter.Count())
}

func TestCollectOpenAIResponseImageOutputSizesFromJSONBytes(t *testing.T) {
	body := []byte(`{
		"output": [
			{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"3840x2160"},
			{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"1024x1024"}
		]
	}`)

	require.Equal(t, 2, CountOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, CollectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIResponseImageOutputSizesFromImagesAPIData(t *testing.T) {
	body := []byte(`{
		"data": [
			{"b64_json":"final-a","size":"2048x1152"},
			{"b64_json":"final-b","size":"2048x1152"}
		]
	}`)

	require.Equal(t, 2, CountOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"2048x1152", "2048x1152"}, CollectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIImageOutputSizesFromSSEBody(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\",\"size\":\"3840x2160\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\"},{\"id\":\"ig_2\",\"type\":\"image_generation_call\",\"result\":\"final-b\",\"size\":\"1024x1024\"}]}}\n\n" +
		"data: [DONE]\n\n"

	require.Equal(t, 2, CountOpenAIImageOutputsFromSSEBody(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, CollectOpenAIImageOutputSizesFromSSEBody(body))
}
