package protocol

import "encoding/json"

type ChatMetadata struct {
	Model    string         `json:"model"`
	Stream   bool           `json:"stream"`
	User     string         `json:"user"`
	Metadata map[string]any `json:"metadata"`
}

func ParseChat(b []byte) (ChatMetadata, error) { var m ChatMetadata; return m, json.Unmarshal(b, &m) }
func RewriteModel(b []byte, upstream string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["model"] = upstream
	return json.Marshal(m)
}
