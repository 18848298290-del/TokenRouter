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
