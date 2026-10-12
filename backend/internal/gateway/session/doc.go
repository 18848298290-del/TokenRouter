// Package session manages gateway session identity, digest matching, continuation state, and cache interfaces.
//
// Reading entry points:
//   - hash.go derives session hashes and sticky seeds from request content and client identity.
//   - digest_session_store.go matches continuation requests against stored digest prefixes.
//   - openai_ws_state_store.go stores Responses session bindings and response state.
//
// File groups:
//   - hash.go, qoder_hash.go, openai_content_seed.go, client_id.go: hashes, content seeds, and client IDs.
//   - gemini_digest.go, anthropic_prompt_cache.go, digest_session_store.go: digests and prompt cache matching.
//   - openai_ws_state_store.go, compat_responses.go, reasoning_history.go: continuation, ciphertext state, and reasoning history.
//   - cyber*.go: cyber session keys, blocks, transcripts, and storage adapters.
//   - isolation.go, http_response_owner.go: session isolation and response ownership.
//   - live.go, codex_turn_origins.go: Live state and Codex turn origins.
//   - stores.go: cache interfaces for sticky sessions, reasoning text, and session control.
package session
