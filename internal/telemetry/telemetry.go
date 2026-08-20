package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultCapacity      = 1024
	DefaultBatchSize     = 10
	DefaultFlushInterval = time.Second
)

type Attempt struct {
	Account    string `json:"account"`
	Adapter    string `json:"adapter"`
	Alias      string `json:"alias,omitempty"`
	Status     int    `json:"status"`
	LatencyMS  int64  `json:"latency_ms"`
	Outcome    string `json:"outcome"`
	CooldownMS int64  `json:"cooldown_ms,omitempty"`
}

type contextKey string

const virtualKeyContextKey contextKey = "virtual-key"

func WithVirtualKey(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, virtualKeyContextKey, name)
}

func VirtualKeyFromContext(ctx context.Context) string {
	name, _ := ctx.Value(virtualKeyContextKey).(string)
	return name
}

type Event struct {
	RequestID      string    `json:"request_id"`
	GatewayID      string    `json:"gateway_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	Endpoint       string    `json:"endpoint"`
	PublicModel    string    `json:"public_model"`
	UpstreamModel  string    `json:"upstream_model"`
	VirtualKey     string    `json:"virtual_key"`
	VirtualKeyID   string    `json:"virtual_key_id,omitempty"`
	FinalStatus    int       `json:"final_status"`
	Success        bool      `json:"success"`
	TotalLatencyMS int64     `json:"total_latency_ms"`
	// OverheadMS and UpstreamTTFBMS are gateway-internal latency metrics.
	// They are excluded from the wire format (json:"-") because the
	// control-plane RequestEventSchema is strict and rejects unknown fields.
	OverheadMS     int64     `json:"-"`
	UpstreamTTFBMS int64     `json:"-"`
	Streaming      bool      `json:"streaming"`
	ConfigVersion  int64     `json:"config_version,omitempty"`
	InputTokens    int64     `json:"input_tokens,omitempty"`
	OutputTokens   int64     `json:"output_tokens,omitempty"`
	TotalTokens    int64     `json:"total_tokens,omitempty"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	Attempts       []Attempt `json:"attempts"`
}

func (e Event) String() string {
	encoded, _ := json.Marshal(e)
	return string(encoded)
}

type Recorder interface {
	Record(Event)
}

type Publisher interface {
	Publish(context.Context, []Event) error
}

type AsyncOptions struct {
	Capacity      int
	BatchSize     int
	FlushInterval time.Duration
}

type AsyncRecorder struct {
	publisher Publisher
	queue     chan Event
	done      chan struct{}
	closed    atomic.Bool
	dropped   atomic.Uint64
	failed    atomic.Uint64
	once      sync.Once
}

func NewAsyncRecorder(publisher Publisher, options AsyncOptions) *AsyncRecorder {
	if options.Capacity <= 0 {
		options.Capacity = DefaultCapacity
	}
	if options.BatchSize <= 0 {
		options.BatchSize = DefaultBatchSize
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = DefaultFlushInterval
	}
	recorder := &AsyncRecorder{
		publisher: publisher,
		queue:     make(chan Event, options.Capacity),
		done:      make(chan struct{}),
	}
	go recorder.run(options.BatchSize, options.FlushInterval)
	return recorder
}

func (r *AsyncRecorder) Record(event Event) {
	if r == nil || r.publisher == nil || r.closed.Load() {
		return
	}
	select {
	case r.queue <- event:
	default:
		r.dropped.Add(1)
	}
}

func (r *AsyncRecorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

func (r *AsyncRecorder) FailedBatches() uint64 {
	if r == nil {
		return 0
	}
	return r.failed.Load()
}

func (r *AsyncRecorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.closed.Store(true)
		close(r.queue)
	})
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *AsyncRecorder) run(batchSize int, flushInterval time.Duration) {
	defer close(r.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := r.publisher.Publish(ctx, batch); err != nil {
			r.failed.Add(1)
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case event, ok := <-r.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, event)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
