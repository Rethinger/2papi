package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"
)

type capturePublisher struct {
	mu      sync.Mutex
	batches [][]Event
	notify  chan struct{}
}

func newCapturePublisher() *capturePublisher {
	return &capturePublisher{notify: make(chan struct{}, 8)}
}

func (p *capturePublisher) Publish(_ context.Context, events []Event) error {
	p.mu.Lock()
	p.batches = append(p.batches, append([]Event(nil), events...))
	p.mu.Unlock()
	p.notify <- struct{}{}
	return nil
}

func (p *capturePublisher) snapshot() [][]Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]Event, len(p.batches))
	for i := range p.batches {
		out[i] = append([]Event(nil), p.batches[i]...)
	}
	return out
}

func TestAsyncRecorderPublishesFullBatch(t *testing.T) {
	publisher := newCapturePublisher()
	recorder := NewAsyncRecorder(publisher, AsyncOptions{
		Capacity:      4,
		BatchSize:     2,
		FlushInterval: time.Hour,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := recorder.Close(ctx); err != nil {
			t.Fatalf("close recorder: %v", err)
		}
	})

	recorder.Record(Event{RequestID: "one"})
	recorder.Record(Event{RequestID: "two"})

	select {
	case <-publisher.notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry batch")
	}
	batches := publisher.snapshot()
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("batches=%+v", batches)
	}
	if batches[0][0].RequestID != "one" || batches[0][1].RequestID != "two" {
		t.Fatalf("unexpected batch order: %+v", batches[0])
	}
}

func TestAsyncRecorderFlushesPartialBatchOnClose(t *testing.T) {
	publisher := newCapturePublisher()
	recorder := NewAsyncRecorder(publisher, AsyncOptions{
		Capacity:      4,
		BatchSize:     10,
		FlushInterval: time.Hour,
	})
	recorder.Record(Event{RequestID: "partial"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatalf("close recorder: %v", err)
	}

	batches := publisher.snapshot()
	if len(batches) != 1 || len(batches[0]) != 1 || batches[0][0].RequestID != "partial" {
		t.Fatalf("partial batch was not flushed: %+v", batches)
	}
}

type failingPublisher struct{}

func (failingPublisher) Publish(context.Context, []Event) error {
	return context.DeadlineExceeded
}

func TestAsyncRecorderCountsFailedBatches(t *testing.T) {
	recorder := NewAsyncRecorder(failingPublisher{}, AsyncOptions{
		Capacity:      1,
		BatchSize:     1,
		FlushInterval: time.Hour,
	})
	recorder.Record(Event{RequestID: "failed"})

	deadline := time.Now().Add(time.Second)
	for recorder.FailedBatches() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if recorder.FailedBatches() != 1 {
		t.Fatalf("failed batches=%d want=1", recorder.FailedBatches())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBatchFitsControlPlaneBound(t *testing.T) {
	if DefaultBatchSize > 10 {
		t.Fatalf("default batch size=%d exceeds control-plane bound", DefaultBatchSize)
	}
}
