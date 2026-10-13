package anthropic

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Provider fingerprints need accepted versions on creation, repair, and
// upgrade, while metadata rewriting needs stable surrounding JSON order.
// Separate cache fixtures expose masked-session reuse and fingerprint write
// counts so tests can check selected identities and persistence decisions for
// the supplied User-Agent cases.

// identityCacheStub retains a session mask while fingerprint reads remain
// absent in metadata-rewrite fixtures.
type identityCacheStub struct {
	maskedSessionID string // stored test session UUID
}

// stubIdentityCache copies fingerprint values and counts writes for provider UA
// creation, upgrade, and repair assertions.
type stubIdentityCache struct {
	// fingerprint and lastSet point to the same value copy after SetFingerprint
	// increments setCalls. GetFingerprint returns another scalar copy, so
	// changes to that returned value need a write before the stored value
	// changes.
	fingerprint *Fingerprint // copied cached fingerprint value
	setCalls    int          // fingerprint write invocation count
	lastSet     *Fingerprint // copied most recent write
}

// GetFingerprint returns an absent fingerprint for this masked-session fixture.
// Requests using this stub therefore exercise metadata masking independently of
// cached fingerprint selection, and the read reports no cache error.
func (s *identityCacheStub) GetFingerprint(_ context.Context, _ int64) (*Fingerprint, error) {
	return nil, nil
}

// SetFingerprint accepts fingerprint writes without retaining them in this
// fixture. The stub's tests inspect masked metadata, so a write succeeds while
// its fingerprint read continues to report an absent value.
func (s *identityCacheStub) SetFingerprint(_ context.Context, _ int64, _ *Fingerprint) error {
	return nil
}

// GetMaskedSessionID returns the fixture's retained mask with a nil error. An
// empty field acts as an absent cache value to RequestFingerprint, which can
// then select a new mask for the rewritten metadata.
func (s *identityCacheStub) GetMaskedSessionID(_ context.Context, _ int64) (string, error) {
	return s.maskedSessionID, nil
}

// SetMaskedSessionID stores the requested mask and returns a nil error.
// Subsequent fixture reads return that value, allowing the masked-metadata case
// to assert the selected session appears in its rewritten body.
func (s *identityCacheStub) SetMaskedSessionID(_ context.Context, _ int64, sessionID string) error {
	s.maskedSessionID = sessionID
	return nil
}

// TestIdentityService_RewriteUserID_PreservesTopLevelFieldOrder rewrites
// JSON-string metadata in a body with ordered neighboring keys. It checks
// literal key order, replacement of the original identity, and the retained
// metadata string shape.
func TestIdentityService_RewriteUserID_PreservesTopLevelFieldOrder(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewRequestFingerprint(cache)

	originalUserID := FormatMetadataUserID(
		"d61f76d0730d2b920763648949bad5c79742155c27037fc77ac3f9805cb90169",
		"",
		"7578cf37-aaca-46e4-a45c-71285d9dbb83",
		"2.1.78",
	)
	body := []byte(`{"alpha":1,"messages":[],"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"stream":true}`)

	result, err := svc.RewriteUserID(body, 123, "acc-uuid", "client-xyz", "claude-cli/2.1.78 (external, cli)")
	require.NoError(t, err)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"messages"`, `"metadata"`, `"max_tokens"`, `"thinking"`, `"output_config"`, `"stream"`)
	require.NotContains(t, resultStr, originalUserID)
	require.Contains(t, resultStr, `"metadata":{"user_id":"`)
}

// TestIdentityService_RewriteUserIDWithMasking_PreservesTopLevelFieldOrder
// supplies a cached mask while rewriting metadata with ordered neighboring
// keys. It checks that the mask appears in the string metadata and top-level
// order remains intact.
func TestIdentityService_RewriteUserIDWithMasking_PreservesTopLevelFieldOrder(t *testing.T) {
	cache := &identityCacheStub{maskedSessionID: "11111111-2222-4333-8444-555555555555"}
	svc := NewRequestFingerprint(cache)

	originalUserID := FormatMetadataUserID(
		"d61f76d0730d2b920763648949bad5c79742155c27037fc77ac3f9805cb90169",
		"",
		"7578cf37-aaca-46e4-a45c-71285d9dbb83",
		"2.1.78",
	)
	body := []byte(`{"alpha":1,"messages":[],"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"stream":true}`)

	result, err := svc.RewriteUserIDWithMasking(context.Background(), body, 123, true, "acc-uuid", "client-xyz", "claude-cli/2.1.78 (external, cli)")
	require.NoError(t, err)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"messages"`, `"metadata"`, `"max_tokens"`, `"thinking"`, `"output_config"`, `"stream"`)
	require.Contains(t, resultStr, cache.maskedSessionID)
	require.True(t, strings.Contains(resultStr, `"metadata":{"user_id":"`))
}

// strconvQuote surrounds a fixture string with quotes after escaping
// backslashes and double quotes. Callers provide the metadata strings used by
// these tests; control characters are not normalized as they would be by a
// general JSON encoder.
func strconvQuote(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

// GetFingerprint returns a value copy of the stored test fingerprint. A missing
// value returns nil without error, and callers can mutate a returned copy
// without changing the fixture until SetFingerprint runs.
func (s *stubIdentityCache) GetFingerprint(_ context.Context, _ int64) (*Fingerprint, error) {
	if s.fingerprint == nil {
		return nil, nil
	}
	clone := *s.fingerprint
	return &clone, nil
}

// SetFingerprint counts the write and stores a value copy for later reads and
// last-write assertions. Callers supply a nonnil fingerprint; its later
// mutations cannot change the stored copy's scalar fields.
func (s *stubIdentityCache) SetFingerprint(_ context.Context, _ int64, fingerprint *Fingerprint) error {
	s.setCalls++
	clone := *fingerprint
	s.lastSet = &clone
	s.fingerprint = &clone
	return nil
}

// GetMaskedSessionID returns an absent mask with a successful read result. This
// fingerprint-selection fixture leaves masking state unconfigured, so any
// masking caller sees the same empty cache value each time.
func (s *stubIdentityCache) GetMaskedSessionID(_ context.Context, _ int64) (string, error) {
	return "", nil
}

// SetMaskedSessionID accepts a mask write while retaining no mask value.
// Subsequent reads remain empty, keeping this fixture focused on the
// fingerprint writes and version-selection behavior asserted by its tests.
func (s *stubIdentityCache) SetMaskedSessionID(_ context.Context, _ int64, _ string) error {
	return nil
}

// fingerprintHeadersWithUA returns a fresh header map containing a nonempty
// User-Agent when supplied. An empty argument leaves that field absent,
// allowing tests to exercise default selection with the same fixture
// constructor.
func fingerprintHeadersWithUA(userAgent string) http.Header {
	headers := http.Header{}
	if userAgent != "" {
		headers.Set("User-Agent", userAgent)
	}
	return headers
}

// TestIsAcceptableFingerprintUserAgent covers the listed CLI and other-product
// version shapes, suffixes, length, overflow, and sentinel cases. Each case
// checks the acceptance result expected for its supplied User-Agent.
func TestIsAcceptableFingerprintUserAgent(t *testing.T) {
	cases := []struct {
		name      string // table scenario display name
		userAgent string // candidate request User-Agent
		want      bool   // expected UA acceptance result
	}{
		{name: "official_cli", userAgent: "claude-cli/2.1.220 (external, cli)", want: true},
		{name: "official_cli_no_meta", userAgent: "claude-cli/2.1.220", want: true},
		{name: "next_major_still_allowed", userAgent: "claude-cli/4.0.0 (external, cli)", want: true},
		{name: "other_product_valid_form", userAgent: "some-sdk/1.2.3 (node)", want: true},
		{name: "local_build_suffix", userAgent: "claude-cli/999.0.0-local (undefined, cli)", want: false},
		{name: "dev_build_suffix", userAgent: "claude-cli/2.1.220-dev (external, cli)", want: false},
		{name: "build_metadata_suffix", userAgent: "claude-cli/2.1.220+build1 (external, cli)", want: false},
		{name: "sentinel_major", userAgent: "claude-cli/999.0.0 (external, cli)", want: false},
		{name: "overflowing_major", userAgent: "claude-cli/999999999999999999999.0.0 (external, cli)", want: false},
		{name: "empty", userAgent: "", want: false},
		{name: "no_version", userAgent: "claude-cli (external, cli)", want: false},
		{name: "two_segment_version", userAgent: "claude-cli/2.1 (external, cli)", want: false},
		{name: "browser_ua", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", want: false},
		{name: "leading_junk", userAgent: "x claude-cli/2.1.220 (external, cli)", want: false},
		{name: "too_long", userAgent: "claude-cli/2.1.220 (" + strings.Repeat("a", 300) + ")", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, IsAcceptableFingerprintUserAgent(testCase.userAgent))
		})
	}
}

// TestGetOrCreateFingerprintRejectsMalformedUserAgentOnCreate supplies a
// local-build sentinel to an empty cache. It checks that the selected and
// persisted UA use the default instead of retaining that invalid version.
func TestGetOrCreateFingerprintRejectsMalformedUserAgentOnCreate(t *testing.T) {
	cache := &stubIdentityCache{}
	service := NewRequestFingerprint(cache)

	fingerprint, err := service.GetOrCreateFingerprint(
		context.Background(), 1,
		fingerprintHeadersWithUA("claude-cli/999.0.0-local (undefined, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, DefaultFingerprint.UserAgent, fingerprint.UserAgent)
	require.NotContains(t, cache.lastSet.UserAgent, "999.0.0")
}

// TestGetOrCreateFingerprintRejectsSentinelVersionOnUpgrade presents a
// malformed sentinel above a healthy cached version. It checks the cached UA is
// returned and no fingerprint write is recorded.
func TestGetOrCreateFingerprintRejectsSentinelVersionOnUpgrade(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.22 (external, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	service := NewRequestFingerprint(cache)

	fingerprint, err := service.GetOrCreateFingerprint(
		context.Background(), 1,
		fingerprintHeadersWithUA("claude-cli/999.0.0-local (undefined, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.22 (external, cli)", fingerprint.UserAgent)
	require.Zero(t, cache.setCalls)
}

// TestGetOrCreateFingerprintStillUpgradesOnValidNewerVersion supplies a valid
// higher version of the cached product. It checks that the selected UA advances
// and the fixture records one persistence write.
func TestGetOrCreateFingerprintStillUpgradesOnValidNewerVersion(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.22 (external, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	service := NewRequestFingerprint(cache)

	newUserAgent := "claude-cli/2.1.223 (external, cli)"
	fingerprint, err := service.GetOrCreateFingerprint(context.Background(), 1, fingerprintHeadersWithUA(newUserAgent))

	require.NoError(t, err)
	require.Equal(t, newUserAgent, fingerprint.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

// TestGetOrCreateFingerprintAcceptsValidUserAgentOnCreate starts with no
// fingerprint and a valid current CLI UA. It checks that UA, a nonempty
// ClientID, and one cache write are produced.
func TestGetOrCreateFingerprintAcceptsValidUserAgentOnCreate(t *testing.T) {
	cache := &stubIdentityCache{}
	service := NewRequestFingerprint(cache)
	userAgent := "claude-cli/" + CLICurrentVersion + " (external, cli)"

	fingerprint, err := service.GetOrCreateFingerprint(context.Background(), 1, fingerprintHeadersWithUA(userAgent))

	require.NoError(t, err)
	require.Equal(t, userAgent, fingerprint.UserAgent)
	require.NotEmpty(t, fingerprint.ClientID)
	require.Equal(t, 1, cache.setCalls)
}

// TestDefaultFingerprintUserAgentIsAcceptable validates the package's
// configured default against the fingerprint admission rule. It checks that
// normal default selection supplies a UA eligible for later provider caching.
func TestDefaultFingerprintUserAgentIsAcceptable(t *testing.T) {
	require.True(t, IsAcceptableFingerprintUserAgent(DefaultFingerprint.UserAgent))
}

// TestGetOrCreateFingerprintHealsPoisonedCacheUsingValidClientUA supplies a
// malformed cached UA with a valid client replacement. It checks one repaired
// write, the selected client UA, removal of the sentinel, and ClientID
// retention.
func TestGetOrCreateFingerprintHealsPoisonedCacheUsingValidClientUA(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/999.0.0-local (undefined, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	service := NewRequestFingerprint(cache)
	realUserAgent := "claude-cli/2.1.22 (external, cli)"

	fingerprint, err := service.GetOrCreateFingerprint(context.Background(), 1, fingerprintHeadersWithUA(realUserAgent))

	require.NoError(t, err)
	require.Equal(t, realUserAgent, fingerprint.UserAgent)
	require.Equal(t, 1, cache.setCalls)
	require.NotContains(t, cache.lastSet.UserAgent, "999.0.0")
	require.Equal(t, "cid-1", fingerprint.ClientID)
}

// TestGetOrCreateFingerprintHealsPoisonedCacheWithoutValidClientUA supplies
// malformed cached and client versions. It checks that repair selects and
// persists the default UA with one fingerprint write.
func TestGetOrCreateFingerprintHealsPoisonedCacheWithoutValidClientUA(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/999.0.0-local (undefined, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	service := NewRequestFingerprint(cache)

	fingerprint, err := service.GetOrCreateFingerprint(
		context.Background(), 1,
		fingerprintHeadersWithUA("claude-cli/999.0.0-local (undefined, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, DefaultFingerprint.UserAgent, fingerprint.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

// TestGetOrCreateFingerprintDoesNotRewriteHealthyCache supplies an older valid
// UA to a recently updated healthy cache. It checks the higher cached version
// remains selected and no persistence write occurs.
func TestGetOrCreateFingerprintDoesNotRewriteHealthyCache(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.220 (external, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	service := NewRequestFingerprint(cache)

	fingerprint, err := service.GetOrCreateFingerprint(
		context.Background(), 1,
		fingerprintHeadersWithUA("claude-cli/2.1.22 (external, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.220 (external, cli)", fingerprint.UserAgent)
	require.Zero(t, cache.setCalls)
}

// TestGetOrCreateFingerprintMissingUserAgentKeepsDefault creates identity from
// an empty header map and empty cache. It checks successful selection of the
// default fingerprint User-Agent for that missing-client case.
func TestGetOrCreateFingerprintMissingUserAgentKeepsDefault(t *testing.T) {
	cache := &stubIdentityCache{}
	service := NewRequestFingerprint(cache)

	fingerprint, err := service.GetOrCreateFingerprint(context.Background(), 1, http.Header{})

	require.NoError(t, err)
	require.Equal(t, DefaultFingerprint.UserAgent, fingerprint.UserAgent)
}
