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

// Question holds the question type and descriptions; other fields pass through in the raw request.
type Question struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

// Request holds fields needed for routing and response validation.
type Request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
	Stream    bool                `json:"stream"`
}

// Response holds the validated response model and optional usage; the executor delivers the raw answers.
type Response struct {
	Model    string
	Usage    protocol.TokenUsage
	HasUsage bool
}

// ParseRequest validates synchronous SystemOne requests with string, object, or array descriptions.
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
			if question.Type == "noul" && len(question.Criteria) == 0 {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(question.Criteria, &criteria) != nil || criteria == nil {
				return request, fmt.Errorf("questions[%q].criteria must be an object", id)
			}
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

// description checks the top-level shape of an already validated JSON value.
func description(raw json.RawMessage, nullable bool) bool {
	raw = bytes.TrimSpace(raw)
	if nullable && bytes.Equal(raw, []byte("null")) {
		return true
	}
	return len(raw) > 0 && (raw[0] == '"' || raw[0] == '{' || raw[0] == '[')
}

// ReplaceModel patches the top-level model while retaining question, answer, and extension bytes.
func ReplaceModel(body []byte, model string) ([]byte, error) {
	return sjson.SetBytes(body, "model", model)
}

// ModerationBody decodes state and question text into a message body for content moderation.
func (r Request) ModerationBody() []byte {
	var content strings.Builder
	appendModerationText(&content, r.State)
	questions, _ := json.Marshal(r.Questions)
	appendModerationText(&content, questions)
	body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": content.String()}}})
	return body
}

// appendModerationText reads keys and values from JSON, decoding strings once and retaining number precision.
func appendModerationText(content *strings.Builder, raw json.RawMessage) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		value, err := decoder.Token()
		if err != nil {
			return
		}
		switch value.(type) {
		case json.Delim, nil:
			continue
		default:
			_, _ = fmt.Fprintln(content, value)
		}
	}
}

// ParseResponse validates answers, then parses usage independently; invalid usage leaves HasUsage false.
func ParseResponse(body []byte, questions map[string]Question) (Response, error) {
	var wire struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   json.RawMessage            `json:"usage"`
	}
	var result Response
	if json.Unmarshal(body, &wire) != nil || strings.TrimSpace(wire.Model) == "" || len(wire.Answers) != len(questions) {
		return result, errors.New("invalid SystemOne response")
	}
	for id, question := range questions {
		var answer struct {
			Type          string             `json:"type"`
			Noul          *float64           `json:"noul"`
			Choice        *string            `json:"choice"`
			Score         *float64           `json:"score"`
			Confidence    *float64           `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
			Legend        json.RawMessage    `json:"legend"`
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
	var usage struct {
		Input  *int `json:"input_tokens"`
		Output *int `json:"output_tokens"`
	}
	if json.Unmarshal(wire.Usage, &usage) == nil && usage.Input != nil && usage.Output != nil && *usage.Input >= 0 && *usage.Output >= 0 && *usage.Input <= int(^uint(0)>>1)-*usage.Output {
		result.HasUsage = true
		result.Usage = protocol.TokenUsage{InputTokens: *usage.Input, OutputTokens: *usage.Output}
	}
	return result, nil
}

// probability reports whether a supplied probability lies between zero and one inclusive.
func probability(value *float64) bool { return value != nil && *value >= 0 && *value <= 1 }

// ProbeBody builds the minimal request shared by manual provider tests and scheduled probes.
func ProbeBody(model, state string) []byte {
	if state == "" {
		state = "The service is available."
	}
	body, _ := json.Marshal(map[string]any{"model": model, "state": state, "questions": map[string]any{"available": map[string]string{"type": "noul", "instructions": "Does the state say the service is available?"}}})
	return body
}
