package protocol

import (
	"bytes"
	"encoding/json"
)

type ChatMetadata struct {
	Model    string         `json:"model"`
	Stream   bool           `json:"stream"`
	User     string         `json:"user"`
	Metadata map[string]any `json:"metadata"`
}

func ParseChat(b []byte) (ChatMetadata, error) { var m ChatMetadata; return m, json.Unmarshal(b, &m) }
func RewriteModel(b []byte, upstream string) ([]byte, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	model, err := json.Marshal(upstream)
	if err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}
