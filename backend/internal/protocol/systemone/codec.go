package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// SystemOne decisions return typed answers separately from token accounting.
// Valid answers remain deliverable when usage is missing or malformed, while
// valid zero usage still establishes accounting evidence. Request validation
// reads the modeled fields and forwarding patches the original JSON body so
// unknown extensions remain outside that typed subset.
//
// Moderation uses decoded state and modeled question descriptions, including
// JSON keys, with strings decoded once and numbers retained as json.Number.
// Response checks require matching answer types and selected value shapes,
// but do not enforce choice membership, score ranges, or probability-map sums.
// Usage needs two nonnegative int counters whose sum fits int; its failure
// leaves HasUsage false without changing valid answers. Callers pass the
// original validated question map to associate returned answers with the request.

// Question holds the decision type and raw descriptions used by request validation.
type Question struct {
	// Type chooses Criteria's shape: noul accepts optional true/false descriptions,
	// choice accepts an option object, and score accepts a level array.
	// Instructions remains a required string, object, or array for every type.
	Type         string          `json:"type"`         // decision question type
	Instructions json.RawMessage `json:"instructions"` // raw decision instructions
	Criteria     json.RawMessage `json:"criteria"`     // type-specific criteria JSON
}

// Request holds synchronous SystemOne fields needed for routing, moderation, and answer validation.
type Request struct {
	Model     string              `json:"model"`     // requested model identifier
	State     json.RawMessage     `json:"state"`     // raw state description
	Questions map[string]Question `json:"questions"` // ID-keyed decision questions
	Stream    bool                `json:"stream"`    // rejected streaming selection
}

// Response holds checked model and accounting evidence for the executor's raw-answer delivery.
type Response struct {
	Model string              // supplied response model
	Usage protocol.TokenUsage // accepted token counters
	// HasUsage records two present, nonnegative, sum-safe usage counters.
	// False leaves valid answers deliverable while the executor treats their
	// accounting as unknown; explicit zero counters can still set this true.
	HasUsage bool
}

// ParseRequest validates a synchronous request with a nonblank model and
// a nonempty question map. State and instructions accept
// JSON strings, objects, or arrays; streaming and unsupported decision labels fail.
//
// Noul criteria can be omitted or contain true/false keys, choice needs 1-255
// options and permits null option descriptions, and score needs 2-10 description
// levels. Returned fields retain their supplied spelling rather than trimming
// model or type values. Errors can return partially decoded request data, so
// callers use the request after success and retain raw JSON for forwarding extras.
// @project-doc docs/interfaces/jev_upstream.md#systemone_execution
func ParseRequest(body []byte) (Request, error) {
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		return request, errors.New("invalid SystemOne JSON request")
	}
	if strings.TrimSpace(request.Model) == "" {
		return request, errors.New("model is required")
	}
	if request.Stream {
		return request, errors.New("SystemOne does not support streaming")
	}
	if !description(request.State, false) {
		return request, errors.New("state must be a string, object, or array")
	}
	if len(request.Questions) == 0 {
		return request, errors.New("questions must be a non-empty object")
	}
	for id, question := range request.Questions {
		if !description(question.Instructions, false) {
			return request, fmt.Errorf("questions[%q].instructions must be a string, object, or array", id)
		}
		switch question.Type {
		case "noul", "choice":
			// A noul decision can rely on its instructions without a criteria
			// object, allowing the minimal availability probe to use the same request validator.
			if question.Type == "noul" && len(question.Criteria) == 0 {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(question.Criteria, &criteria) != nil || criteria == nil {
				return request, fmt.Errorf("questions[%q].criteria must be an object", id)
			}
			// JSON object decoding imposes no choice-count restriction. Check
			// cardinality here before the validated request's raw criteria can
			// be forwarded by the executor.
			if question.Type == "choice" && (len(criteria) == 0 || len(criteria) > 255) {
				return request, fmt.Errorf("questions[%q].criteria requires 1 to 255 options", id)
			}
			for name, value := range criteria {
				if question.Type == "noul" && name != "true" && name != "false" {
					return request, fmt.Errorf("questions[%q].criteria keys must be true or false", id)
				}
				if !description(value, question.Type == "choice") {
					return request, fmt.Errorf("questions[%q].criteria contains an invalid description", id)
				}
			}
		case "score":
			var levels []json.RawMessage
			// JSON array shape alone permits any number of score descriptions.
			// Apply the level-count rule before raw criteria enters execution
			// through a successfully validated request.
			if json.Unmarshal(question.Criteria, &levels) != nil || len(levels) < 2 || len(levels) > 10 {
				return request, fmt.Errorf("questions[%q].criteria requires 2 to 10 levels", id)
			}
			for _, value := range levels {
				if !description(value, false) {
					return request, fmt.Errorf("questions[%q].criteria contains an invalid description", id)
				}
			}
		default:
			return request, fmt.Errorf("questions[%q].type must be noul, choice, or score", id)
		}
	}
	return request, nil
}

// description reports an accepted top-level description shape after trimming.
// It expects already validated JSON and checks the first byte for string,
// object, or array; nullable additionally accepts the exact null literal.
func description(raw json.RawMessage, nullable bool) bool {
	raw = bytes.TrimSpace(raw)
	if nullable && bytes.Equal(raw, []byte("null")) {
		return true
	}
	return len(raw) > 0 && (raw[0] == '"' || raw[0] == '{' || raw[0] == '[')
}

// ReplaceModel patches the top-level model with sjson and returns its write
// error to the caller. Other fields stay outside full-object decoding;
// callers validate complete JSON separately because the patcher can attempt
// edits on malformed input.
func ReplaceModel(body []byte, model string) ([]byte, error) {
	return sjson.SetBytes(body, "model", model)
}

// ModerationBody returns one user message containing decoded state and the
// modeled question map for content moderation. Callers normally use a Request
// accepted by ParseRequest; unknown question extensions are absent from the
// typed map and therefore outside this extraction.
//
// Token decoding includes keys and primitive values while omitting delimiters
// and null values. Strings decode once, preserving literal backslash sequences,
// and json.Number retains large numeric spelling. Question serialization
// errors are suppressed; final body encoding uses string-only content. Manually
// supplied invalid raw descriptions can contribute only their decoded text prefix.
func (r Request) ModerationBody() []byte {
	var content strings.Builder
	appendModerationText(&content, r.State)
	// Invalid manually constructed raw question fields can fail serialization.
	// Retain decoded state text for the moderation body even when that question
	// representation cannot contribute further tokens.
	questions, _ := json.Marshal(r.Questions)
	appendModerationText(&content, questions)
	// The final body contains fixed map keys and strings, which the standard
	// encoder can serialize even when invalid raw question fields above supplied
	// no question text. Its error result is discarded for this supported shape.
	body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": content.String()}}})
	return body
}

// appendModerationText appends decoded keys and primitive values to a nonnil
// builder, one token per line, until decoding ends or fails. Delimiters and
// null are omitted; caller-supplied malformed JSON can leave a text prefix.
//
// UseNumber retains numeric spelling before formatting, and Token decodes
// strings once so a literal escaped backslash does not become another Unicode
// decoding pass. Callers own the builder's accumulated content and validate
// description shapes before using that text for request moderation.
func appendModerationText(content *strings.Builder, raw json.RawMessage) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// State or questions can contain 9007199254740993. Retain that spelling
	// during token extraction so moderation receives the submitted numeric text
	// before any float64 rounding could change a keyword match.
	decoder.UseNumber()
	for {
		value, err := decoder.Token()
		if err != nil {
			// EOF finishes the description; another decoding failure retains the
			// already appended tokens so this helper does not discard earlier readable state.
			return
		}
		switch value.(type) {
		case json.Delim, nil:
			continue
		default:
			// Separate decoded primitives with newlines for the moderation message.
			// strings.Builder writes return no error, so discarding Fprintln's error
			// cannot lose a successfully decoded token through a failed write.
			_, _ = fmt.Fprintln(content, value)
		}
	}
}

// ParseResponse checks response model and answers against the supplied question
// map, then reads usage independently. Callers pass their validated request
// questions; malformed envelopes, mismatched answer counts/types, or missing
// selected answer fields return an error before accounting is accepted.
//
// Noul requires a probability in [0,1]. Choice and score require confidence
// in that range and a nonempty probability map, but selected choice membership,
// score range, and map sums are not checked. Valid answers return their supplied
// model even when usage fails; HasUsage becomes true only for two present,
// nonnegative int counters whose sum fits int, including explicit zero counts.
func ParseResponse(body []byte, questions map[string]Question) (Response, error) {
	var wire struct {
		Model   string                     `json:"model"`   // supplied response model
		Answers map[string]json.RawMessage `json:"answers"` // ID-keyed raw answers
		Usage   json.RawMessage            `json:"usage"`   // independent usage JSON
	}
	var result Response
	if json.Unmarshal(body, &wire) != nil || strings.TrimSpace(wire.Model) == "" || len(wire.Answers) != len(questions) {
		return result, errors.New("invalid SystemOne response")
	}
	for id, question := range questions {
		// Type selects Noul, Choice, or Score. Confidence and Probabilities
		// are required for choice/score; Legend is shape-checked for score.
		// Pointer fields distinguish missing values from supplied zero values.
		var answer struct {
			Type          string             `json:"type"`          // returned decision type
			Noul          *float64           `json:"noul"`          // optional noul probability
			Choice        *string            `json:"choice"`        // optional selected choice
			Score         *float64           `json:"score"`         // optional continuous score
			Confidence    *float64           `json:"confidence"`    // optional confidence probability
			Probabilities map[string]float64 `json:"probabilities"` // returned probability map
			Legend        json.RawMessage    `json:"legend"`        // raw score descriptions
		}
		if json.Unmarshal(wire.Answers[id], &answer) != nil || answer.Type != question.Type {
			return result, errors.New("invalid SystemOne answer")
		}
		valid := false
		switch question.Type {
		case "noul":
			valid = probability(answer.Noul)
		case "choice":
			valid = answer.Choice != nil && probability(answer.Confidence) && len(answer.Probabilities) > 0
		case "score":
			valid = answer.Score != nil && probability(answer.Confidence) && len(answer.Probabilities) > 0 && description(answer.Legend, false)
		}
		if !valid {
			return result, errors.New("invalid SystemOne answer values")
		}
	}
	result.Model = wire.Model
	// Input and Output supply accounting independently of answer validity.
	// Both counters need nonnegative values and a sum that fits int so later
	// usage aggregation can add them without overflowing this response's total.
	var usage struct {
		Input  *int `json:"input_tokens"`  // optional input-token observation
		Output *int `json:"output_tokens"` // optional output-token observation
	}
	if json.Unmarshal(wire.Usage, &usage) == nil && usage.Input != nil && usage.Output != nil && *usage.Input >= 0 && *usage.Output >= 0 && *usage.Input <= int(^uint(0)>>1)-*usage.Output {
		result.HasUsage = true
		result.Usage = protocol.TokenUsage{InputTokens: *usage.Input, OutputTokens: *usage.Output}
	}
	return result, nil
}

// probability reports whether a nonnil float lies in the inclusive [0,1]
// range. Missing values and comparisons outside that range return false,
// letting answer validation distinguish absent or unusable probability evidence.
func probability(value *float64) bool { return value != nil && *value >= 0 && *value <= 1 }

// ProbeBody builds the single-noul request used by provider tests and scheduled
// probes. Empty state selects the service-availability sentence; nonempty state
// and model are serialized as supplied, leaving model validity to the caller.
func ProbeBody(model, state string) []byte {
	if state == "" {
		state = "The service is available."
	}
	// Probe fields contain strings and fixed maps, which have no unsupported
	// values or cycles. The caller receives their encoded availability request
	// directly because this known shape cannot produce an encoding error.
	body, _ := json.Marshal(map[string]any{"model": model, "state": state, "questions": map[string]any{"available": map[string]string{"type": "noul", "instructions": "Does the state say the service is available?"}}})
	return body
}
