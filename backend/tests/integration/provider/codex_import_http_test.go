//go:build integration

package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
)

// TestCodexImportHTTPDatabaseContract checks HTTP import, same-batch updates, and partial failure using
// PostgreSQL and the admin service; privacy jobs are disabled.
func TestCodexImportHTTPDatabaseContract(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	store := newProviderStoreContract(client, integrationDB, nil)
	options := provider.AdminOptions{Creation: provider.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: func() string { return "00000000-0000-4000-8000-000000000001" }}, Credentials: provider.CreateCredentialHooks{Validate: func(context.Context, *provider.Record) error { return nil }, ValidateEdit: func(context.Context, *provider.Record, bool) error { return nil }, Site: func(*provider.Record) (string, error) { return "", nil }}, Background: func(string, func()) bool { return false }, Error: func(string, ...any) {}}
	admin := provider.NewAdmin(store, options)
	archive := provider.NewArchive(admin, nil, provider.ArchiveOptions{})
	invalidated := []int64{}
	importer := provider.NewCodexImporter(admin, archive, provider.CodexImportOptions{Now: time.Now, OAuthClientID: "local-client", Invalidate: func(_ context.Context, v *provider.Record) error { invalidated = append(invalidated, v.ID); return nil }})
	h := providerhttp.NewCodexImportHandler(importer)
	router := gin.New()
	router.POST("/import/codex-session", h.ImportCodexSession)
	user := time.Now().Format("150405.000000000")
	source := map[string]any{"access_token": "fixture-at", "refresh_token": "fixture-rt", "chatgpt_account_id": "test-team-" + user, "chatgpt_user_id": "test-user-" + user}
	request := func(contents []any) provider.CodexSessionImportResult {
		t.Helper()
		raw, err := json.Marshal(contents)
		require.NoError(t, err)
		payload, err := json.Marshal(provider.CodexSessionImportRequest{Content: string(raw)})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/import/codex-session", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
		var envelope struct {
			Code int                               `json:"code"`
			Data provider.CodexSessionImportResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		require.Zero(t, envelope.Code)
		require.NotContains(t, w.Body.String(), "fixture-at")
		require.NotContains(t, w.Body.String(), "fixture-rt")
		return envelope.Data
	}
	first := request([]any{source})
	require.Equal(t, 1, first.Created)
	require.Zero(t, first.Failed)
	id := first.Items[0].ProviderID
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(id).Exec(context.Background())) })
	before, err := store.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "fixture-rt", before.Credentials["refresh_token"])
	require.Empty(t, invalidated)
	source["access_token"] = "fixture-at-new"
	source["refresh_token"] = "fixture-rt-new"
	second := request([]any{source, map[string]any{"unsupported": "invalid-entry"}})
	require.Equal(t, 1, second.Updated)
	require.Equal(t, 1, second.Failed)
	require.Equal(t, id, second.Items[0].ProviderID)
	require.Equal(t, []int64{id}, invalidated)
	after, err := store.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "fixture-rt-new", after.Credentials["refresh_token"])
	require.Equal(t, "local-client", after.Credentials["client_id"])
	require.Equal(t, before.Status, after.Status)
}
