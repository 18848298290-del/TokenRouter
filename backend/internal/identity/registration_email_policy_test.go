package identity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TestNormalizeRegistrationEmailSuffixWhitelist supplies duplicate, uppercase
// and wildcard entries for an administrator update. It checks first-occurrence
// ordering, lowercase normalization and omission of the equivalent duplicate suffix.
func TestNormalizeRegistrationEmailSuffixWhitelist(t *testing.T) {
	got, err := identity.NormalizeRegistrationEmailSuffixWhitelist([]string{"example.com", "@EXAMPLE.COM", " @foo.bar ", "*.EDU.CN"})
	require.NoError(t, err)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

// TestNormalizeRegistrationEmailSuffixWhitelist_Invalid submits malformed host
// and wildcard entries separately in strict mode. Every sampled item must return
// a validation error instead of becoming a persisted whitelist value.
func TestNormalizeRegistrationEmailSuffixWhitelist_Invalid(t *testing.T) {
	for _, item := range []string{"@invalid_domain", "*.", "*", "*.@", "*.foo"} {
		t.Run(item, func(t *testing.T) {
			_, err := identity.NormalizeRegistrationEmailSuffixWhitelist([]string{item})
			require.Error(t, err)
		})
	}
}

// TestParseRegistrationEmailSuffixWhitelist mixes usable saved entries with
// invalid domain syntax in stored JSON. It checks that tolerant runtime parsing
// returns the normalized usable subset in input order.
func TestParseRegistrationEmailSuffixWhitelist(t *testing.T) {
	got := identity.ParseRegistrationEmailSuffixWhitelist(`["example.com","@foo.bar","*.EDU.CN","@invalid_domain","*.foo"]`)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

// TestIsRegistrationEmailSuffixAllowed contrasts exact domain entries with
// wildcard base and subdomain matches. It checks allowed and rejected domains,
// then verifies acceptance when the whitelist is empty.
func TestIsRegistrationEmailSuffixAllowed(t *testing.T) {
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@example.com", []string{"@example.com"}))
	require.False(t, identity.IsRegistrationEmailSuffixAllowed("user@sub.example.com", []string{"@example.com"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@qq.com", []string{"@qq.com"}))
	require.False(t, identity.IsRegistrationEmailSuffixAllowed("user@sub.qq.com", []string{"@qq.com"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("student@cs.edu.cn", []string{"*.edu.cn"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("student@edu.cn", []string{"*.edu.cn"}))
	require.False(t, identity.IsRegistrationEmailSuffixAllowed("student@foo.cn", []string{"*.edu.cn"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@a.com", []string{"@a.com", "*.b.cn"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@school.b.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@b.cn", []string{"@a.com", "*.b.cn"}))
	require.False(t, identity.IsRegistrationEmailSuffixAllowed("user@c.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, identity.IsRegistrationEmailSuffixAllowed("user@any.com", []string{}))
}

// TestIsRegistrationEmailSuffixLimited compares missing, matching and rejecting
// whitelist policies for supplied addresses. It requires a limited result only
// when a nonempty whitelist rejects the address's suffix.
func TestIsRegistrationEmailSuffixLimited(t *testing.T) {
	require.False(t, identity.IsRegistrationEmailSuffixLimited("user@custom.example", nil))
	require.False(t, identity.IsRegistrationEmailSuffixLimited("user@example.com", []string{"@example.com"}))
	require.True(t, identity.IsRegistrationEmailSuffixLimited("user@custom.example", []string{"@example.com"}))
}

// TestRegistrationEmailDomainUsesRegistrableDomain supplies related subdomains
// and a domain using a multi-label public suffix. It checks their shared
// registrable quota keys and removal of a trailing domain dot.
func TestRegistrationEmailDomainUsesRegistrableDomain(t *testing.T) {
	require.Equal(t, "abc.com", identity.RegistrationEmailDomain("user@abc.com"))
	require.Equal(t, "abc.com", identity.RegistrationEmailDomain("user@sub.abc.com"))
	require.Equal(t, "example.co.uk", identity.RegistrationEmailDomain("user@team.example.co.uk"))
	require.Equal(t, "example.com", identity.RegistrationEmailDomain("user@team.example.com."))
}

// TestNormalizeRegistrationEmailAddress checks mailbox uniqueness keys for
// Gmail aliases, non-Gmail dots and unusual local parts. It requires alias folding
// where configured, retained meaningful local characters and empty invalid input.
func TestNormalizeRegistrationEmailAddress(t *testing.T) {
	tests := []struct {
		name  string // case description label
		email string // supplied mailbox address
		want  string // expected registration identity
	}{
		{name: "Gmail 点号与标签", email: "Y.o.u.r.N.a.m.e+abc@Gmail.com", want: "yourname@gmail.com"},
		{name: "Googlemail 同族与根点", email: "your.name+promo@GoogleMail.com.", want: "yourname@gmail.com"},
		{name: "非 Gmail 保留点号", email: "First.Last+promo@QQ.com", want: "first.last@qq.com"},
		{name: "非 Gmail 无点号是不同身份", email: "firstlast@qq.com", want: "firstlast@qq.com"},
		{name: "首字符加号不折叠", email: "+alice@gmail.com", want: "+alice@gmail.com"},
		{name: "纯点号本地部分不折叠为空", email: "...@gmail.com", want: "...@gmail.com"},
		{name: "非法邮箱", email: "invalid-email", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, identity.NormalizeRegistrationEmailAddress(tt.email))
		})
	}
}
