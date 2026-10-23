package upstream

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// TestRewriteImageRequestModes exercises mode conversion in JSON and multipart.
// Model changes, duplicate transport fields, uploaded images, masks, and unrelated
// extension values must survive or be removed according to the selected capability.
func TestRewriteImageRequestModes(t *testing.T) {
	for _, mode := range []protocol.ImageStreamMode{protocol.ImageModeBoth, protocol.ImageModeStreaming, protocol.ImageModeNonStreaming} {
		for _, clientStream := range []bool{false, true} {
			for _, multipartBody := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/client_%t/multipart_%t", mode, clientStream, multipartBody), func(t *testing.T) {
					original := []byte(fmt.Sprintf(`{"model":"original","stream":%t,"partial_images":2,"extension":9007199254740993}`, clientStream))
					contentType := "application/json"
					if multipartBody {
						var buffer bytes.Buffer
						writer := multipart.NewWriter(&buffer)
						require.NoError(t, writer.WriteField("model", "original"))
						require.NoError(t, writer.WriteField("stream", strconv.FormatBool(clientStream)))
						require.NoError(t, writer.WriteField("stream", strconv.FormatBool(clientStream)))
						require.NoError(t, writer.WriteField("partial_images", "2"))
						for _, name := range []string{"image", "mask"} {
							part, err := writer.CreateFormFile(name, name+".png")
							require.NoError(t, err)
							_, err = part.Write([]byte(name + "-bytes"))
							require.NoError(t, err)
						}
						require.NoError(t, writer.Close())
						original = buffer.Bytes()
						contentType = writer.FormDataContentType()
					}
					body, outputType, err := RewriteImageRequest(original, contentType, "mapped", mode, clientStream)
					require.NoError(t, err)
					fields := map[string][]string{}
					if multipartBody {
						_, params, err := mime.ParseMediaType(outputType)
						require.NoError(t, err)
						reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
						for {
							part, err := reader.NextPart()
							if err == io.EOF {
								break
							}
							require.NoError(t, err)
							value, err := io.ReadAll(part)
							require.NoError(t, err)
							fields[part.FormName()] = append(fields[part.FormName()], string(value))
						}
						require.Equal(t, []string{"image-bytes"}, fields["image"])
						require.Equal(t, []string{"mask-bytes"}, fields["mask"])
					} else {
						require.Equal(t, "9007199254740993", gjson.GetBytes(body, "extension").Raw)
						for _, key := range []string{"model", "stream", "partial_images"} {
							if value := gjson.GetBytes(body, key); value.Exists() {
								fields[key] = []string{value.String()}
							}
						}
					}
					require.Equal(t, []string{"mapped"}, fields["model"])
					if mode == protocol.ImageModeNonStreaming {
						require.Empty(t, fields["stream"])
						require.Empty(t, fields["partial_images"])
					} else {
						for _, stream := range fields["stream"] {
							require.Equal(t, strconv.FormatBool(mode.UpstreamStream(clientStream)), stream)
						}
						partial := "2"
						if mode == protocol.ImageModeStreaming && !clientStream {
							partial = "0"
						}
						require.Equal(t, []string{partial}, fields["partial_images"])
					}
				})
			}
		}
	}
}

// TestRewriteImageRequestDuplicateControlFields supplies duplicate JSON transport keys.
// A forced nonstream provider must receive no stream flag regardless of which
// duplicate a compatible upstream JSON parser chooses to interpret.
func TestRewriteImageRequestDuplicateControlFields(t *testing.T) {
	input := []byte(`{"model":"gpt-image-2","stream":true,"stream":true,"partial_images":2,"partial_images":3}`)
	body, _, err := RewriteImageRequest(input, "application/json", "gpt-image-2", protocol.ImageModeNonStreaming, true)
	require.NoError(t, err)

	require.False(t, gjson.GetBytes(body, "stream").Exists())
	require.False(t, gjson.GetBytes(body, "partial_images").Exists())
}

// TestRewriteImageRequestDuplicateStreamingMode canonicalizes repeated and escaped
// transport keys for a streaming-only endpoint. Nested vendor fields and integers
// above JavaScript's precise range survive while the outgoing stream mode is unique.
func TestRewriteImageRequestDuplicateStreamingMode(t *testing.T) {
	input := []byte(`{"stream":false,"str\u0065am":true,"partial_images":3,"partial_images":2,"metadata":{"stream":false,"large":9007199254740993}}`)
	for _, contentType := range []string{"application/json", ""} {
		body, _, err := RewriteImageRequest(input, contentType, "", protocol.ImageModeStreaming, false)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(body, "stream").Bool())
		require.EqualValues(t, 0, gjson.GetBytes(body, "partial_images").Int())
		require.Equal(t, "9007199254740993", gjson.GetBytes(body, "metadata.large").Raw)
		require.False(t, gjson.GetBytes(body, "metadata.stream").Bool())
	}
}
