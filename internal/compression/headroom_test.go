package compression

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
)

func TestPruneForHeadroom(t *testing.T) {
	// Build a payload with many tool messages exceeding reserve
	msgs := []map[string]interface{}{{"role": "system", "content": "you are helpful"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, map[string]interface{}{"role": "user", "content": strings.Repeat("hello world ", 20)})
		msgs = append(msgs, map[string]interface{}{"role": "tool", "content": strings.Repeat("tool result line\n", 50)})
	}
	root := map[string]interface{}{"model": "gpt-dev", "messages": msgs}
	body, _ := json.Marshal(root)
	// Reserve very low to trigger pruning
	reserve := 2000 // ~8k chars
	pruned, saved, wasPruned := PruneForHeadroom(body, reserve, 4)
	if !wasPruned {
		t.Fatalf("expected pruning")
	}
	if saved <= 0 {
		t.Fatalf("saved should be >0 got %d", saved)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(pruned, &out); err != nil {
		t.Fatal(err)
	}
	var outMsgs []message
	if err := json.Unmarshal(out["messages"], &outMsgs); err != nil {
		t.Fatal(err)
	}
	// Should keep system + last 4
	if len(outMsgs) != 1+4 {
		t.Fatalf("kept %d msgs want 5 (1 system +4)", len(outMsgs))
	}
	if outMsgs[0].Role != "system" {
		t.Fatalf("first should be system got %s", outMsgs[0].Role)
	}
	// Small payload should not prune
	small := []byte(`{"model":"gpt-dev","messages":[{"role":"user","content":"hi"}]}`)
	_, _, wasPruned = PruneForHeadroom(small, 120000, 8)
	if wasPruned {
		t.Fatalf("small should not prune")
	}
}

func TestShouldHeadroom(t *testing.T) {
	global := &config.Optimization{Headroom: true, HeadroomReserve: 5000}
	model := &config.Optimization{Headroom: false}
	vk := &config.Optimization{Headroom: true, HeadroomReserve: 1000}
	ok, reserve, _ := ShouldHeadroom(global, model, vk, "")
	if !ok || reserve != 1000 {
		t.Fatalf("vk should win, got ok=%v reserve=%d", ok, reserve)
	}
	ok, _, _ = ShouldHeadroom(global, nil, nil, "true")
	if !ok {
		t.Fatal("header true should enable")
	}
	ok, _, _ = ShouldHeadroom(global, nil, nil, "false")
	if ok {
		t.Fatal("header false should disable")
	}
}

func TestShouldRTK(t *testing.T) {
	global := &config.Optimization{RTKCompression: true}
	if !ShouldRTK(global, nil, nil, "") {
		t.Fatal("global true")
	}
	if ShouldRTK(&config.Optimization{}, nil, nil, "") {
		t.Fatal("global false should not")
	}
	if !ShouldRTK(&config.Optimization{}, nil, nil, "true") {
		t.Fatal("header true")
	}
	vk := &config.Optimization{RTKCompression: true}
	if !ShouldRTK(&config.Optimization{}, nil, vk, "") {
		t.Fatal("vk true")
	}
}
