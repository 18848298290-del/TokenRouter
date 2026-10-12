// Package messageforward prepares Messages upstream requests, forwards and converts them, and handles responses and failures.
//
// Reading entry points:
//   - runtime.go: Messages, token counting, and Chat/Responses conversion entry points.
//   - attempt.go: model preparation, credentials, and state for one forwarding attempt.
//   - exchange.go: upstream request construction, retries, and response options.
//
// File groups:
//   - contracts.go, preparation.go: dependency interfaces, runtime options, and construction.
//   - requests.go, mimic.go, metadata.go, cache_policy.go: headers, client identity, and cache policies.
//   - bedrock.go, passthrough.go, conversion.go, count.go, search.go: platform handling, conversion, counting, and search.
//   - responses.go, errors.go, transport.go, compatibility.go: responses, health observations, and compatibility errors.
package messageforward
