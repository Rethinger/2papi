package compression

import (
	"bytes"
	"encoding/json"
)

// OptimizeRequest applies the three body optimizers — headroom pruning, RTK
// tool-result compression and the caveman system directive — in ONE JSON
// parse/marshal pass over the request body (vitok 7/9: single-pass pipeline,
// replaces the sequential parse-per-optimizer flow in proxy.Endpoint).
//
// The caller resolves the per-request decisions first (DecideHeadroom /
// DecideRTK / DecideCaveman, plus auto refinement) and passes concrete
// parameters. Semantics are byte-identical to the legacy sequential chain:
// headroom prunes first, RTK compresses the surviving messages, caveman
// injects last; when headroom/RTK modify the body the messages are rebuilt
// from the typed form (same as the old sequential re-marshal), otherwise the
// original raw messages pass through untouched so unknown per-message fields
// survive caveman-only requests.

type OptimizeOptions struct {
	// Headroom parameters (resolved profile/defaults; 0 = run with defaults).
	Headroom        bool
	HeadroomKeep    int
	HeadroomReserve int

	// RTK parameters. RTKAuto switches to per-block auto parameters
	// (rtk_mode=auto); RTKParams is ignored then.
	RTK       bool
	RTKAuto   bool
	RTKParams RTKParams

	// Caveman directive text (DirectiveFor(mode) resolved by the caller).
	Caveman          bool
	CavemanDirective string
	// CavemanMode is the concrete mode applied (echoed to the client when
	// the directive is injected; empty = no echo).
	CavemanMode string
}

type OptimizeResult struct {
	Changed             bool
	HeadroomPruned      bool
	HeadroomSavedTokens int
	RTKSavedBytes       int
	CavemanInjected     bool
	// CavemanMode echoes the concrete caveman mode on injection.
	CavemanMode string
}

func (o OptimizeOptions) empty() bool {
	return !o.Headroom && !o.RTK && !o.Caveman
}

func (o OptimizeOptions) headroomParams() (keep, reserve int) {
	keep, reserve = o.HeadroomKeep, o.HeadroomReserve
	if keep <= 0 {
		keep = DefaultHeadroomKeep
	}
	if reserve <= 0 {
		reserve = DefaultHeadroomReserve
	}
	return keep, reserve
}

// OptimizeRequest runs the single-pass pipeline. It returns the original
// body untouched when nothing applies or the payload is not a chat message
// body (headroom/RTK need `messages`; caveman alone still handles the
// Responses `instructions` form).
func OptimizeRequest(body []byte, o OptimizeOptions) ([]byte, OptimizeResult) {
	var res OptimizeResult
	if o.empty() {
		return body, res
	}

	// Fast path: skip the parse when no enabled pass can possibly fire. Each
	// pass has an O(1) precondition on the raw bytes — RTK needs a body at
	// least MinCompressBytes, headroom needs the estimate to reach the reserve,
	// caveman can always inject — and both size gates are re-checked below, so
	// a body failing all of them would parse, walk, and return unchanged. That
	// parse is the whole cost: an explicit headroom profile on a 97 KiB body
	// spent ~8.8ms reaching a no-op, while `headroom auto` (which resolves
	// against the same estimate before setting the flag) spent 0.02ms.
	if !o.Caveman {
		rtkPossible := o.RTK && len(body) >= MinCompressBytes
		headroomPossible := false
		if o.Headroom {
			_, reserve := o.headroomParams()
			headroomPossible = estimatedTokens(body) >= reserve
		}
		if !rtkPossible && !headroomPossible {
			return body, res
		}
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, res
	}

	rawMsgs, haveMessages := root["messages"]
	var msgs []message
	if haveMessages {
		if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
			haveMessages = false
		}
	}

	// 1. Headroom: prune old history first (keeps all system messages).
	if o.Headroom && haveMessages && len(msgs) > 0 {
		keep, reserve := o.headroomParams()
		if estimatedTokens(body) >= reserve && len(msgs) > keep+2 {
			var systemMsgs, otherMsgs []message
			for _, m := range msgs {
				if m.Role == "system" {
					systemMsgs = append(systemMsgs, m)
				} else {
					otherMsgs = append(otherMsgs, m)
				}
			}
			if len(otherMsgs) > keep {
				pruned := append(systemMsgs, otherMsgs[len(otherMsgs)-keep:]...)
				if len(pruned) < len(msgs) {
					msgs = pruned
					res.HeadroomPruned = true
					res.Changed = true
				}
			}
		}
	}

	// 2. RTK: compress large tool/user text contents (idempotent via marker).
	if o.RTK && haveMessages {
		for i := range msgs {
			m := &msgs[i]
			if m.Role != "tool" && m.Role != "user" {
				continue
			}
			if len(m.Content) == 0 {
				continue
			}
			text, err := unmarshalString(m.Content)
			if err != nil {
				continue
			}
			params := o.RTKParams
			if o.RTKAuto {
				params = AutoRTKParamsForBlock(len(text))
			}
			if len(text) < params.MinBytes {
				continue
			}
			compressed, saved := compressText(text, params)
			if saved <= 0 {
				continue
			}
			raw, err := json.Marshal(compressed)
			if err != nil {
				continue
			}
			m.Content = raw
			res.RTKSavedBytes += saved
			res.Changed = true
		}
	}

	// Rebuild the messages array once if headroom/RTK modified it.
	var messagesOut []json.RawMessage
	if (res.HeadroomPruned || res.RTKSavedBytes > 0) && haveMessages {
		reencoded, err := json.Marshal(msgs)
		if err != nil {
			return body, res
		}
		var rawList []json.RawMessage
		if err := json.Unmarshal(reencoded, &rawList); err != nil {
			return body, res
		}
		messagesOut = rawList
	} else if haveMessages {
		_ = json.Unmarshal(rawMsgs, &messagesOut)
	}

	// 3. Caveman: inject the terse directive into the system prompt.
	if o.Caveman {
		directive := o.CavemanDirective
		if directive == "" {
			directive = CavemanDirective
		}
		if haveMessages {
			updated, injected := injectCavemanMessages(messagesOut, directive)
			if injected {
				messagesOut = updated
				res.CavemanInjected = true
				res.CavemanMode = o.CavemanMode
				res.Changed = true
			}
		} else if raw, ok := root["instructions"]; ok && len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			// Responses API form: `instructions` string or array of parts.
			var s string
			if json.Unmarshal(raw, &s) == nil {
				root["instructions"], _ = json.Marshal(s + "\n\n" + directive)
				res.CavemanInjected = true
				res.CavemanMode = o.CavemanMode
				res.Changed = true
			} else {
				var parts []contentPart
				if json.Unmarshal(raw, &parts) == nil {
					root["instructions"], _ = json.Marshal(append(parts, contentPart{Type: "text", Text: directive}))
					res.CavemanInjected = true
					res.CavemanMode = o.CavemanMode
					res.Changed = true
				}
			}
		}
	}

	if !res.Changed {
		return body, res
	}

	if haveMessages {
		reencoded, err := json.Marshal(messagesOut)
		if err != nil {
			return body, res
		}
		root["messages"] = reencoded
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, res
	}
	if res.HeadroomPruned {
		res.HeadroomSavedTokens = estimatedTokens(body) - estimatedTokens(out)
		if res.HeadroomSavedTokens <= 0 {
			res.HeadroomSavedTokens = 0
		}
	}
	return out, res
}

// unmarshalString decodes a JSON string value.
func unmarshalString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// injectCavemanMessages appends the directive to the first system message or
// prepends one, mirroring the legacy InjectCavemanDirectiveWith semantics on
// the raw message list.
func injectCavemanMessages(messages []json.RawMessage, directive string) ([]json.RawMessage, bool) {
	modified := false
	out := make([]json.RawMessage, 0, len(messages)+1)
	for _, raw := range messages {
		if !modified {
			var msg openAIMessageLike
			if err := json.Unmarshal(raw, &msg); err == nil && msg.Role == "system" {
				var text string
				if json.Unmarshal(msg.Content, &text) == nil {
					updated, _ := json.Marshal(map[string]any{"role": "system", "content": text + "\n\n" + directive})
					out = append(out, updated)
					modified = true
					continue
				}
				var parts []contentPart
				if json.Unmarshal(msg.Content, &parts) == nil {
					updated, _ := json.Marshal(map[string]any{"role": "system", "content": append(parts, contentPart{Type: "text", Text: directive})})
					out = append(out, updated)
					modified = true
					continue
				}
			}
		}
		out = append(out, raw)
	}
	if !modified {
		prepend, _ := json.Marshal(map[string]any{"role": "system", "content": directive})
		out = append([]json.RawMessage{prepend}, out...)
		modified = true
	}
	return out, modified
}
