package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpsWSHelpers(t *testing.T) {
	prefixes, invalid := parseTrustedProxyList("10.0.0.0/8,invalid")
	require.Len(t, prefixes, 1)
	require.Len(t, invalid, 1)

	host := hostWithoutPort("example.com:443")
	require.Equal(t, "example.com", host)

	addr := netip.MustParseAddr("10.0.0.1")
	require.True(t, isAddrInTrustedProxies(addr, prefixes))
	require.False(t, isAddrInTrustedProxies(netip.MustParseAddr("192.168.0.1"), prefixes))
}

// TestOpsWSBrandNegotiation checks selection of sub2api-admin or tokenrouter-admin during the handshake.
func TestOpsWSBrandNegotiation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	for _, protocols := range [][]string{
		{"sub2api-admin", "jwt.fixture"},
		{"tokenrouter-admin", "jwt.fixture"},
		{"sub2api-admin", "tokenrouter-admin", "jwt.fixture"},
	} {
		dialer := websocket.Dialer{Subprotocols: protocols}
		conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(t, err)
		want := "tokenrouter-admin"
		if len(protocols) == 2 && protocols[0] == "sub2api-admin" {
			want = "sub2api-admin"
		}
		require.Equal(t, want, conn.Subprotocol())
		require.NoError(t, conn.Close())
	}
}
