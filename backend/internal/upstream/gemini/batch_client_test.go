package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBatchClientLocalTLSAndStreamCancellation(t *testing.T) {
	streamStarted := make(chan struct{})
	streamStopped := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "fixture-key", r.Header.Get("x-goog-api-key"))
		switch {
		case r.URL.Path == "/upload/v1beta/files":
			require.Equal(t, "multipart", r.URL.Query().Get("uploadType"))
			require.NoError(t, r.ParseMultipartForm(1<<20))
			file, _, err := r.FormFile("file")
			require.NoError(t, err)
			data, err := io.ReadAll(file)
			_ = file.Close()
			require.NoError(t, err)
			require.Contains(t, string(data), `"key":"request-one"`)
			_, _ = io.WriteString(w, `{"file":{"name":"files/f1"}}`)
		case r.URL.Path == "/v1beta/models/gemini-image:batchGenerateContent":
			_, _ = io.WriteString(w, `{"name":"batches/b1","state":"JOB_STATE_PENDING"}`)
		case r.URL.Path == "/v1beta/batches/b1":
			_, _ = io.WriteString(w, `{"name":"batches/b1","state":"JOB_STATE_SUCCEEDED","dest":{"fileName":"files/f1"}}`)
		case r.URL.Path == "/v1beta/batches/b1:cancel":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1beta/files/f1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1beta/files/f1":
			_, _ = fmt.Fprintf(w, `{"downloadUri":"https://%s/bytes","mimeType":"application/jsonl"}`, r.Host)
		case r.URL.Path == "/bytes":
			w.Header().Set("Content-Type", "application/jsonl")
			// Advertise one unsent byte so cancellation is distinguishable from a complete download.
			w.Header().Set("Content-Length", fmt.Sprint(len("first-line\n")+1))
			_, _ = io.WriteString(w, "first-line\n")
			_ = http.NewResponseController(w).Flush()
			close(streamStarted)
			<-r.Context().Done()
			close(streamStopped)
		default:
			t.Errorf("未知调用: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewGeminiBatchHTTPClient(server.URL, server.Client(), errors.New("missing fixture key"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploaded, err := client.UploadJSONL(ctx, "fixture-key", "fixture", strings.NewReader("{\"key\":\"request-one\"}\n"))
	require.NoError(t, err)
	require.Equal(t, "files/f1", uploaded.Name)
	created, err := client.CreateBatch(ctx, "fixture-key", "gemini-image", uploaded.Name, "fixture")
	require.NoError(t, err)
	require.Equal(t, "batches/b1", created.Name)
	batch, err := client.GetBatch(ctx, "fixture-key", created.Name)
	require.NoError(t, err)
	require.Equal(t, "JOB_STATE_SUCCEEDED", batch.State)
	require.NoError(t, client.CancelBatch(ctx, "fixture-key", created.Name))
	require.NoError(t, client.DeleteFile(ctx, "fixture-key", uploaded.Name))
	body, mime, err := client.DownloadFile(ctx, "fixture-key", uploaded.Name)
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	require.Equal(t, "application/jsonl", mime)
	select {
	case <-streamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("下载没有开始")
	}
	data := make([]byte, len("first-line\n"))
	_, err = io.ReadFull(body, data)
	require.NoError(t, err)
	cancel()
	_, err = io.ReadAll(body)
	require.Error(t, err)
	_ = body.Close()
	select {
	case <-streamStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("取消未释放上游下载")
	}
}
