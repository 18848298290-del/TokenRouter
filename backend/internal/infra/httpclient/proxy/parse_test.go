package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Proxy parsing fixtures distinguish direct blank input, accepted authority
// and credentials, the SOCKS alias, and invalid URL forms. Password checks
// cover the missing-host diagnostic separately from credential retention on
// valid URLs; these tests make no network or authentication requests.

// TestParse_空字符串直连 supplies empty proxy text to Parse. It requires no error,
// empty normalized text, and a nil parsed URL, protecting the direct-access
// result for absent configuration.
func TestParse_空字符串直连(t *testing.T) {
	trimmed, parsed, err := Parse("")
	if err != nil {
		t.Fatalf("空字符串应直连: %v", err)
	}
	if trimmed != "" {
		t.Errorf("trimmed 应为空: got %q", trimmed)
	}
	if parsed != nil {
		t.Errorf("parsed 应为 nil: got %v", parsed)
	}
}

// TestParse_空白字符串直连 supplies whitespace-only proxy text to Parse. It requires
// the same empty and nil result as absent configuration, protecting trimming
// before direct-access selection.
func TestParse_空白字符串直连(t *testing.T) {
	trimmed, parsed, err := Parse("   ")
	if err != nil {
		t.Fatalf("空白字符串应直连: %v", err)
	}
	if trimmed != "" {
		t.Errorf("trimmed 应为空: got %q", trimmed)
	}
	if parsed != nil {
		t.Errorf("parsed 应为 nil: got %v", parsed)
	}
}

// TestParse_有效HTTP代理 supplies a valid HTTP authority with an explicit port.
// It requires successful parsing, exact returned text, and the same host
// authority on the parsed URL.
func TestParse_有效HTTP代理(t *testing.T) {
	trimmed, parsed, err := Parse("http://proxy.example.com:8080")
	if err != nil {
		t.Fatalf("有效 HTTP 代理应成功: %v", err)
	}
	if trimmed != "http://proxy.example.com:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
	require.NotNil(t, parsed)
	if parsed.Host != "proxy.example.com:8080" {
		t.Errorf("Host 不匹配: got %q", parsed.Host)
	}
}

// TestParse_有效HTTPS代理 supplies a valid HTTPS authority with its port. It
// requires successful parsing and the retained HTTPS scheme, protecting
// acceptance of encrypted proxy transport selection.
func TestParse_有效HTTPS代理(t *testing.T) {
	_, parsed, err := Parse("https://proxy.example.com:443")
	if err != nil {
		t.Fatalf("有效 HTTPS 代理应成功: %v", err)
	}
	if parsed.Scheme != "https" {
		t.Errorf("Scheme 不匹配: got %q", parsed.Scheme)
	}
}

// TestParse_有效SOCKS5代理_自动升级为SOCKS5H supplies the SOCKS5 alias to Parse. It
// requires both returned text and parsed scheme to use SOCKS5H, protecting
// alias conversion for routing consumers.
func TestParse_有效SOCKS5代理_自动升级为SOCKS5H(t *testing.T) {
	trimmed, parsed, err := Parse("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("有效 SOCKS5 代理应成功: %v", err)
	}
	// Normalize the SOCKS5 alias to SOCKS5H.
	if trimmed != "socks5h://127.0.0.1:1080" {
		t.Errorf("trimmed 应升级为 socks5h: got %q", trimmed)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
}

// TestParse_无效URL supplies malformed URL syntax to Parse. It requires a
// returned error containing the invalid-proxy diagnostic, protecting parser
// failure before any routing value is accepted.
func TestParse_无效URL(t *testing.T) {
	_, _, err := Parse("://invalid")
	if err == nil {
		t.Fatal("无效 URL 应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid proxy URL") {
		t.Errorf("错误信息应包含 'invalid proxy URL': got %s", err.Error())
	}
}

// TestParse_缺少Host supplies HTTP proxy text with no authority host. It
// requires an error identifying missing host, protecting rejection of
// configuration that cannot identify a proxy server.
func TestParse_缺少Host(t *testing.T) {
	_, _, err := Parse("http://")
	if err == nil {
		t.Fatal("缺少 host 应返回错误")
	}
	if !strings.Contains(err.Error(), "missing host") {
		t.Errorf("错误信息应包含 'missing host': got %s", err.Error())
	}
}

// TestParse_不支持的Scheme supplies an FTP proxy URL with an otherwise present
// authority. It requires an unsupported-scheme diagnostic, protecting the
// supported proxy protocol allowlist.
func TestParse_不支持的Scheme(t *testing.T) {
	_, _, err := Parse("ftp://proxy.example.com:21")
	if err == nil {
		t.Fatal("不支持的 scheme 应返回错误")
	}
	if !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Errorf("错误信息应包含 'unsupported proxy scheme': got %s", err.Error())
	}
}

// TestParse_含密码URL脱敏 supplies valid SOCKS credentials and a separate
// missing-host URL carrying a password. It requires userinfo retention on
// valid normalization and password omission from the missing-host error.
func TestParse_含密码URL脱敏(t *testing.T) {
	// Normalization must retain credentials on a valid SOCKS URL.
	trimmed, parsed, err := Parse("socks5://user:secret_password@proxy.local:1080")
	if err != nil {
		t.Fatalf("含密码的有效 URL 应成功: %v", err)
	}
	if trimmed == "" || parsed == nil {
		t.Fatal("应返回非空结果")
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
	if !strings.HasPrefix(trimmed, "socks5h://") {
		t.Errorf("trimmed 应以 socks5h:// 开头: got %q", trimmed)
	}
	if parsed.User == nil {
		t.Error("升级后应保留 UserInfo")
	}

	// A missing host must produce a diagnostic with the password redacted.
	_, _, err = Parse("http://user:secret_password@:0/")
	if err == nil {
		t.Fatal("缺少 host 应返回错误")
	}
	if strings.Contains(err.Error(), "secret_password") {
		t.Error("错误信息不应包含明文密码")
	}
	if !strings.Contains(err.Error(), "missing host") {
		t.Errorf("错误信息应包含 'missing host': got %s", err.Error())
	}
}

// TestParse_带空白的有效URL supplies a valid HTTP proxy surrounded by spaces. It
// requires trimmed returned text and a non-nil parsed URL, protecting
// construction from harmless surrounding whitespace.
func TestParse_带空白的有效URL(t *testing.T) {
	trimmed, parsed, err := Parse("  http://proxy.example.com:8080  ")
	if err != nil {
		t.Fatalf("带空白的有效 URL 应成功: %v", err)
	}
	if trimmed != "http://proxy.example.com:8080" {
		t.Errorf("trimmed 应去除空白: got %q", trimmed)
	}
	if parsed == nil {
		t.Fatal("parsed 不应为 nil")
	}
}

// TestParse_Scheme大小写不敏感 supplies uppercase SOCKS5 and HTTP spellings through
// URL parsing. It requires SOCKS5H normalized text and acceptance of HTTP,
// protecting the supported mixed-case input examples.
func TestParse_Scheme大小写不敏感(t *testing.T) {
	// Uppercase SOCKS5 also normalizes to SOCKS5H.
	trimmed, parsed, err := Parse("SOCKS5://proxy.example.com:1080")
	if err != nil {
		t.Fatalf("大写 SOCKS5 应被接受: %v", err)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("大写 SOCKS5 Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
	if !strings.HasPrefix(trimmed, "socks5h://") {
		t.Errorf("大写 SOCKS5 trimmed 应升级为 socks5h://: got %q", trimmed)
	}

	// Uppercase HTTP is accepted.
	_, _, err = Parse("HTTP://proxy.example.com:8080")
	if err != nil {
		t.Fatalf("大写 HTTP 应被接受: %v", err)
	}
}

// TestParse_带认证的有效代理 supplies HTTP userinfo with username and password. It
// requires a parsed userinfo value and exact returned URL text, protecting
// credential retention for the routing consumer.
func TestParse_带认证的有效代理(t *testing.T) {
	trimmed, parsed, err := Parse("http://user:pass@proxy.example.com:8080")
	if err != nil {
		t.Fatalf("带认证的代理 URL 应成功: %v", err)
	}
	if parsed.User == nil {
		t.Error("应保留 UserInfo")
	}
	if trimmed != "http://user:pass@proxy.example.com:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
}

// TestParse_IPv6地址 supplies a bracketed IPv6 HTTP authority with an explicit
// port. It requires the IPv6 hostname and retained returned spelling,
// protecting accepted authority parsing before pool normalization.
func TestParse_IPv6地址(t *testing.T) {
	trimmed, parsed, err := Parse("http://[::1]:8080")
	if err != nil {
		t.Fatalf("IPv6 代理 URL 应成功: %v", err)
	}
	if parsed.Hostname() != "::1" {
		t.Errorf("Hostname 不匹配: got %q", parsed.Hostname())
	}
	if trimmed != "http://[::1]:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
}

// TestParse_SOCKS5H保持不变 supplies an already normalized SOCKS5H URL. It
// requires identical returned text and scheme, protecting acceptance without
// another alias transformation of that scheme.
func TestParse_SOCKS5H保持不变(t *testing.T) {
	trimmed, parsed, err := Parse("socks5h://proxy.local:1080")
	if err != nil {
		t.Fatalf("有效 SOCKS5H 代理应成功: %v", err)
	}
	// An existing SOCKS5H scheme requires no alias conversion.
	if trimmed != "socks5h://proxy.local:1080" {
		t.Errorf("trimmed 不应变化: got %q", trimmed)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应保持 socks5h: got %q", parsed.Scheme)
	}
}

// TestParse_无Scheme裸地址 supplies a bare host-and-port string with no URL
// authority syntax. It requires an error, protecting callers from treating an
// unqualified address as an accepted proxy URL.
func TestParse_无Scheme裸地址(t *testing.T) {
	// A bare address lacks URL authority, so Host is empty.
	_, _, err := Parse("proxy.example.com:8080")
	if err == nil {
		t.Fatal("无 scheme 的裸地址应返回错误")
	}
}
