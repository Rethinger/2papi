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
