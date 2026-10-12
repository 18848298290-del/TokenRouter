// Package openaiforward forwards OpenAI-compatible requests and converts Anthropic protocols.
//
// Reading entry points:
//   - prelude.go: request preparation and protocol and transport selection.
//   - chat.go: Chat Completions conversion and upstream response handling.
//   - http.go: HTTP exchanges and retries on the same provider.
//
// File groups:
//   - requests.go, transform.go: upstream request construction and body transformation.
//   - messages.go, passthrough.go: Messages and passthrough execution.
//   - raw_chat*.go, raw_fallback*.go: Chat forwarding and protocol conversion.
//   - anthropic*.go: Anthropic requests, stream reading, response conversion, and usage.
//   - result_projection.go: conversion between gateway, compatibility, and package result types.
package openaiforward
