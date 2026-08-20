CREATE TABLE IF NOT EXISTS request_events (
  id bigserial PRIMARY KEY,
  gateway_id text NOT NULL,
  request_id text NOT NULL,
  occurred_at timestamptz NOT NULL,
  endpoint text NOT NULL,
  public_model text NOT NULL,
  upstream_model text NOT NULL DEFAULT '',
  virtual_key text NOT NULL DEFAULT '',
  streaming boolean NOT NULL DEFAULT false,
  config_version bigint NOT NULL DEFAULT 0,
  final_status integer NOT NULL CHECK (final_status BETWEEN 0 AND 599),
  success boolean NOT NULL DEFAULT false,
  total_latency_ms bigint NOT NULL CHECK (total_latency_ms >= 0),
  input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  total_tokens bigint NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
  received_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (gateway_id, request_id)
);

CREATE INDEX IF NOT EXISTS request_events_occurred_at_idx ON request_events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS request_events_model_occurred_idx ON request_events (public_model, occurred_at DESC);
CREATE INDEX IF NOT EXISTS request_events_status_occurred_idx ON request_events (final_status, occurred_at DESC);

CREATE TABLE IF NOT EXISTS request_event_attempts (
  request_event_id bigint NOT NULL REFERENCES request_events(id) ON DELETE CASCADE,
  position integer NOT NULL CHECK (position >= 0),
  account text NOT NULL,
  adapter text NOT NULL,
  status integer NOT NULL CHECK (status BETWEEN 0 AND 599),
  outcome text NOT NULL CHECK (outcome IN ('success','rate_limited','upstream_error','saturated','canceled','rejected')),
  latency_ms bigint NOT NULL CHECK (latency_ms >= 0),
  cooldown_ms bigint NOT NULL DEFAULT 0 CHECK (cooldown_ms >= 0),
  PRIMARY KEY (request_event_id, position)
);

CREATE INDEX IF NOT EXISTS request_event_attempts_account_idx ON request_event_attempts (account);
