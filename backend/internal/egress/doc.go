// Package egress manages outbound proxies, TLS fingerprints, and request transport policies.
// Provider reassignment follows configured expiration fallback, and request
// selection copies cached TLS settings before handing them to transport adapters.
// Services retain cache and worker lifecycles while network adapters perform
// probes, capture ClientHello records, and construct request headers.
//
// Reading entry points:
//   - proxy_admin.go: proxy queries, updates, connectivity probes, and quality checks.
//   - request_policy.go: per-request proxy, TLS, header, and destination validation settings.
//   - tls_router_service.go: cached TLS routers and request-to-profile matching.
//
// File groups:
//   - proxy*.go: proxy data, administration, transfers, expiration, and stream failure isolation.
//   - tls*.go: TLS profiles, routing rules, caches, copying, and request selection.
//   - collector*.go: TLS capture records, short-lived sessions, and record limits.
//   - request_policy.go, http2_policy.go, openai_ws_policy.go: request policies and HTTP/2 and WebSocket selection.
//   - url_policy.go, operator_url.go, usage_url.go, monitor_hosts.go: URL, host allowlist, and usage monitor validation.
//   - ollama*.go, crs_proxy.go: Ollama cloud URLs and session cookies, and CRS proxy matching and creation.
//   - header_overrides.go, response_headers.go, cloudflare.go: request overrides, response headers, and Cloudflare detection.
//   - diagnostics.go: caller-supplied logging.
package egress
