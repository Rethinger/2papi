package compression

import (
	"bytes"
	"encoding/json"
)

// CavemanDirective is a condensed version of the viral "caveman" system prompt
// (JuliusBrussee/caveman, adapted by 9Router): reply terse, keep all technical
// substance, drop only fluff. Measured savings: up to 65% output tokens.
const CavemanDirective = `Respond terse like smart caveman. All technical substance stay. Only fluff die.

Drop articles, filler (just/really/basically/simply), pleasantries, hedging. Fragments OK. Short synonyms (big not extensive). No tool-call narration, no decorative tables/emoji, no dumping long raw error logs unless asked — quote shortest decisive line. Standard well-known acronyms OK (DB/API/HTTP); never invent new abbreviations. Technical terms exact. Code blocks unchanged. Errors quoted exact.

Never drop not/never/no/only/except — flip meaning worse than any token saved. Numbers and units exact.

Tool calls: fire direct. No preamble, plan, or progress note before or between calls. After result: next call direct or final answer — never announce next call.

Preserve user's dominant language exactly — compress the style, not the language.

No self-reference. Never name or announce the style. Pattern: [thing] [action] [reason]. [next step].

Drop caveman for security warnings, irreversible action confirmations, or multi-step sequences where omitted conjunctions risk misread; resume after.`

type openAIMessageLike struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// InjectCavemanDirective appends the caveman directive to the request's system
// prompt (OpenAI chat `messages` or Responses API `instructions`). It returns
// (body, true, nil) when the body was modified; (body, false, nil) when the
// payload has no injectable system prompt; and (body, false, err) on invalid
// JSON so callers can skip without breaking the request.
func InjectCavemanDirective(body []byte) ([]byte, bool, error) {
	var payload struct {
		Messages     []json.RawMessage `json:"messages"`
		Instructions json.RawMessage   `json:"instructions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false, err
	}

	// OpenAI Responses format: `instructions` is a string or an array of parts.
	if len(payload.Instructions) > 0 && !bytes.Equal(payload.Instructions, []byte("null")) {
		var s string
		if json.Unmarshal(payload.Instructions, &s) == nil {
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				return body, false, err
			}
			out["instructions"] = s + "\n\n" + CavemanDirective
			updated, err := json.Marshal(out)
			return updated, err == nil, err
		}
		var parts []contentPart
		if json.Unmarshal(payload.Instructions, &parts) == nil {
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				return body, false, err
			}
			out["instructions"] = append(parts, contentPart{Type: "text", Text: CavemanDirective})
			updated, err := json.Marshal(out)
			return updated, err == nil, err
		}
		return body, false, nil
	}

	// OpenAI chat format: append to the first system message, or prepend one.
	if payload.Messages == nil {
		return body, false, nil
	}
	modified := false
	messages := make([]json.RawMessage, 0, len(payload.Messages)+1)
	for _, raw := range payload.Messages {
		if !modified {
			var msg openAIMessageLike
			if err := json.Unmarshal(raw, &msg); err == nil && msg.Role == "system" {
				var text string
				if json.Unmarshal(msg.Content, &text) == nil {
					updated, _ := json.Marshal(map[string]any{"role": "system", "content": text + "\n\n" + CavemanDirective})
					messages = append(messages, updated)
					modified = true
					continue
				}
				var parts []contentPart
				if json.Unmarshal(msg.Content, &parts) == nil {
					updated, _ := json.Marshal(map[string]any{"role": "system", "content": append(parts, contentPart{Type: "text", Text: CavemanDirective})})
					messages = append(messages, updated)
					modified = true
					continue
				}
			}
		}
		messages = append(messages, raw)
	}
	if !modified {
		// No system message: prepend one so the directive still applies.
		prepend, _ := json.Marshal(map[string]any{"role": "system", "content": CavemanDirective})
		messages = append([]json.RawMessage{prepend}, messages...)
		modified = true
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return body, false, err
	}
	out["messages"] = messages
	updated, err := json.Marshal(out)
	return updated, modified, err
}
