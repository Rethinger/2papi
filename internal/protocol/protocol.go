package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type ChatMetadata struct {
	Model    string         `json:"model"`
	Stream   bool           `json:"stream"`
	User     string         `json:"user"`
	Metadata map[string]any `json:"metadata"`
}

type EndpointMetadata = ChatMetadata

func ParseEndpoint(b []byte) (EndpointMetadata, error) {
	var m EndpointMetadata
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return m, errors.New("trailing json tokens")
	}
	return m, nil
}
func ParseChat(b []byte) (ChatMetadata, error) { return ParseEndpoint(b) }
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

func RewriteModel(b []byte, upstream string) ([]byte, error) {
	return RewriteModelAndThinking(b, upstream, 0)
}

func RewriteModelAndThinking(b []byte, upstream string, thinkingBudget int) ([]byte, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if upstream != "" {
		model, err := json.Marshal(upstream)
		if err != nil {
			return nil, err
		}
		m["model"] = model
	}

	if thinkingBudget > 0 {
		th := ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: thinkingBudget,
		}
		thRaw, err := json.Marshal(th)
		if err == nil {
			m["thinking"] = thRaw
		}

		var currentMax int
		if rawMax, ok := m["max_tokens"]; ok {
			_ = json.Unmarshal(rawMax, &currentMax)
		} else if rawMax, ok := m["max_completion_tokens"]; ok {
			_ = json.Unmarshal(rawMax, &currentMax)
		}

		minRequired := thinkingBudget + 1024
		if currentMax < minRequired {
			maxRaw, _ := json.Marshal(minRequired)
			m["max_tokens"] = maxRaw
		}
	}

	return json.Marshal(m)
}
