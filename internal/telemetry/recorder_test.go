package telemetry

import (
	"sync"
)

type recordingRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingRecorder) Record(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}
