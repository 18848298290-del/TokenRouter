package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// binaryFixtureClient maps trusted asset requests to local fixtures.
type binaryFixtureClient struct {
	url       string
	downloads *[]string
}

func (c binaryFixtureClient) DownloadFile(ctx context.Context, url, dest string, max int64) error {
	if c.downloads != nil {
		*c.downloads = append(*c.downloads, url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/archive", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, max))
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

func (c binaryFixtureClient) FetchChecksumFile(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/checksum", nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return io.ReadAll(res.Body)
}

func TestBinaryInstallAndRollback(t *testing.T) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "sub2api", Mode: 0o755, Size: 3}))
	_, err := tw.Write([]byte("new"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	name := "tokenrouter_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	sum := fmt.Sprintf("%x  %s\n", sha256.Sum256(buffer.Bytes()), name)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksum" {
			_, _ = io.WriteString(w, sum)
		} else {
			_, _ = w.Write(buffer.Bytes())
		}
	}))
	defer server.Close()
	assets := []ops.Asset{{Name: name, DownloadURL: "https://github.com/fixture/" + name}, {Name: "checksums.txt", DownloadURL: "https://github.com/fixture/checksums.txt"}}
	exe := filepath.Join(t.TempDir(), "server")
	require.NoError(t, os.WriteFile(exe, []byte("old"), 0o755))
	var downloads []string
	installer := NewBinaryInstaller(binaryFixtureClient{url: server.URL, downloads: &downloads}, func() (string, error) { return exe, nil })
	legacyName := "sub2api_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	assets = append(assets, ops.Asset{Name: legacyName, DownloadURL: "https://github.com/fixture/" + legacyName})
	require.NoError(t, installer.Apply(context.Background(), assets))
	require.Equal(t, "https://github.com/fixture/"+name, downloads[0])
	data, err := os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
	require.NoError(t, installer.Rollback())
	data, err = os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	// Failure of the second rename must restore the previous executable.
	calls := 0
	installer.rename = func(a, b string) error {
		calls++
		if calls == 2 {
			return errors.New("planned replace failure")
		}
		return os.Rename(a, b)
	}
	require.ErrorContains(t, installer.Apply(context.Background(), assets), "restored backup")
	data, err = os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	installer.rename = os.Rename
	sum = "bad  " + name + "\n"
	require.ErrorContains(t, installer.Apply(context.Background(), assets), "checksum mismatch")
	data, err = os.ReadFile(exe)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	// Legacy releases use legacy archive names, which must still match their checksum entries.
	sum = fmt.Sprintf("%x  %s\n", sha256.Sum256(buffer.Bytes()), legacyName)
	require.NoError(t, installer.Apply(context.Background(), assets[1:]))
	require.Equal(t, "https://github.com/fixture/"+legacyName, downloads[len(downloads)-1])
	require.NoError(t, installer.Rollback())
}

// TestBinaryArchiveNameCompatibility checks both names and prefers tokenrouter regardless of entry order.
func TestBinaryArchiveNameCompatibility(t *testing.T) {
	for _, names := range [][]string{{"sub2api"}, {"tokenrouter"}, {"sub2api", "tokenrouter"}, {"tokenrouter", "sub2api"}} {
		var buffer bytes.Buffer
		tw := tar.NewWriter(&buffer)
		for _, name := range names {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(name))}))
			_, err := tw.Write([]byte(name))
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		dir := t.TempDir()
		archive := filepath.Join(dir, "fixture.tar")
		require.NoError(t, os.WriteFile(archive, buffer.Bytes(), 0o600))
		dest := filepath.Join(dir, "installed")
		installer := NewBinaryInstaller(nil, nil)
		require.NoError(t, installer.extractBinary(archive, dest))
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		want := "tokenrouter"
		if len(names) == 1 {
			want = names[0]
		}
		require.Equal(t, want, string(got))
	}
}
