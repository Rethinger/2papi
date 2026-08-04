# Dashboard and Control Plane Design

Date: 2026-08-04
Status: Draft for user review
Related design: `docs/superpowers/specs/2026-08-04-multi-account-ai-gateway-design.md`

## 1. Goal

Add a full local single-admin dashboard and durable control plane to the existing Go gateway. The dashboard must manage providers, accounts, models, routes, virtual API keys, budgets, usage, audit records, settings, layouts, and custom widgets. PostgreSQL and Redis must remain outside the gateway request hot path.

## 2. Confirmed product choices

- Access mode: local single administrator without login for the first version.
- Feature scope: complete management, health, usage, costs, budgets, audit, settings, and configuration import/export.
- Visual direction: dark card-first account manager.
- Overview: user-configurable grid with drag-and-drop, resize, widget library, multiple named layouts, reset, import, and export.
- Customization level: system widgets plus custom REST/JSON and read-only SQL widgets.
- Custom widgets are declarative. Arbitrary JavaScript and HTML are not supported.
- Authentication, users, teams, roles, SSO, payments, and multi-region control plane are deferred.

## 3. Architecture

### 3.1 Components

- **Next.js control plane:** dashboard UI, management API, widget execution API, snapshot compiler, usage queries, and local administration.
- **PostgreSQL:** durable source of truth for providers, encrypted credentials, accounts, deployments, models, routes, virtual keys, budgets, settings, layouts, widget definitions, usage rollups, and audit events.
- **Redis:** configuration-version notifications, gateway acknowledgements, live health state, cooldown/circuit state projection, widget-result cache, query deduplication, and short-lived usage counters.
- **Go gateway:** latency-sensitive API traffic, immutable in-memory snapshots, health and usage event emission, and snapshot acknowledgement.
- **Usage worker:** batches request events into PostgreSQL rollups without blocking the request path. It may initially run inside the control-plane process but must use a bounded queue interface that can become a separate worker.

### 3.2 Trust boundary

The first version has no user login and therefore must bind the dashboard to loopback by default. Exposing it beyond localhost requires an explicit configuration flag and a warning. The management API is not public and is isolated from the OpenAI-compatible API.

Control-plane-to-gateway calls use an internal service token. PostgreSQL and Redis are private Compose services without host-published ports by default.

### 3.3 Hot-path rule

The Go gateway never queries PostgreSQL for request routing. It receives a fully compiled, versioned snapshot, validates it, and swaps an atomic pointer only after successful validation. Redis notifications announce new versions but do not contain plaintext credentials.

## 4. Navigation and screens

### 4.1 Overview

A card-first configurable workspace. The user can:

- drag and resize widgets;
- add, remove, duplicate, and configure widgets;
- save multiple named layouts;
- switch layouts without losing changes;
- reset a layout to a built-in default;
- import and export layout JSON;
- select refresh intervals per widget;
- pause an expensive widget;
- see stale, loading, empty, and error states independently per widget.

Default widgets:

- gateway health;
- account cards with quota, latency, health, active load, cooldown, and circuit state;
- request volume;
- success/error rate;
- time to first token and total latency;
- token usage and estimated cost;
- active cooldowns and open circuits;
- model traffic distribution;
- budget consumption;
- recent request feed;
- configuration version and gateway acknowledgement status.

### 4.2 Providers and accounts

Providers define adapter type, display metadata, supported credential fields, capabilities, and optional discovery behavior. Accounts hold one provider credential set and routing attributes.

The UI supports:

- provider selection and custom OpenAI-compatible endpoints;
- credential creation and replacement;
- connection validation and model discovery;
- labels, region, outbound proxy reference, priority, weight, tier, cost metadata, and concurrency limit;
- manual enable/disable;
- health, quota confidence, reset time, latency, recent errors, cooldown, and circuit state;
- safe credential rotation without returning existing secret values.

### 4.3 Models and routes

- public model aliases;
- upstream model deployments;
- capability declarations, including streaming, tools, vision, JSON mode, embeddings, and context limits;
- candidate account pools;
- routing strategy and fallback tiers;
- per-route retry budget and timeout policy;
- visual route ordering;
- validation that every alias has at least one eligible deployment;
- preview showing which account would be selected for a simulated request.

### 4.4 Virtual API keys

- create a virtual key and display the plaintext once;
- store only a keyed hash;
- model allowlists;
- RPM, TPM, concurrency, and budget limits;
- optional routing-policy override;
- enable, disable, rotate, and revoke;
- last-used time, usage, cost, and recent errors.

### 4.5 Usage and costs

- filters by time, provider, account, model, route, virtual key, status, and error class;
- requests, input/output tokens, cache tokens, estimated cost, TTFT, total latency, retries, and fallback count;
- hourly and daily rollups;
- request metadata view without prompt/body logging by default;
- CSV and JSON export;
- configurable provider/model pricing history so old usage preserves historical cost.

### 4.6 Budgets

- global, account, model, route, and virtual-key budgets;
- daily, monthly, and rolling windows;
- soft warning threshold and hard action;
- actions: notify in UI, throttle, block, or route to fallback tier;
- budget status widgets and audit records.

### 4.7 Audit

Append-only records for credential operations, account/provider changes, model and route changes, virtual-key actions, budgets, widget definitions, layouts, settings, imports, exports, snapshot publication, and gateway acknowledgement.

Secrets and full request bodies never appear in audit payloads.

### 4.8 Settings

- local bind addresses and ports;
- internal service token rotation;
- master encryption key status;
- data retention;
- request metadata logging;
- provider pricing sources;
- default timeouts and resilience values;
- outbound endpoint allowlist;
- backup, restore, configuration import/export;
- system diagnostics and version information.

## 5. Custom widget system

### 5.1 Definition

A widget definition contains:

- id, name, description, and version;
- source type and source configuration;
- parameters and optional dashboard filters;
- transform pipeline;
- visualization type and options;
- refresh interval and cache TTL;
- timeout and row/item limits;
- layout defaults and minimum/maximum dimensions;
- enabled state and last validation result.

Definitions are validated against a versioned JSON Schema.

### 5.2 Source types

#### Built-in metrics

Queries predefined analytics services and gateway live state. These are trusted and use typed APIs.

#### REST/JSON

- requests execute only from the server;
- destination hosts and resolved IPs must pass an allowlist and SSRF checks;
- redirects are revalidated;
- localhost, private, link-local, metadata, and Unix-socket targets are blocked unless explicitly allowlisted;
- secret headers are referenced by secret id and never stored in widget JSON;
- methods are limited to GET and POST;
- request and response sizes are bounded;
- response content type and JSON shape are validated;
- timeouts and concurrency limits are mandatory.

#### Read-only SQL

- a dedicated PostgreSQL role can access only approved analytics views;
- only one `SELECT` statement is accepted;
- writes, DDL, `COPY`, functions with side effects, system catalogs, multiple statements, and transaction controls are rejected;
- every query has a statement timeout and row limit;
- parameters use bound variables;
- query results are size-limited and normalized before reaching the browser.

### 5.3 Transformations

Declarative operations only:

- path selection;
- rename and type coercion;
- filter and sort;
- group and aggregate;
- arithmetic expressions from a restricted expression grammar;
- date bucketing;
- limit and top-N;
- status threshold mapping.

No JavaScript, HTML, template execution, filesystem access, shell commands, or dynamic module loading.

### 5.4 Visualizations

- metric;
- status;
- progress/gauge;
- table;
- recent-event feed;
- line, area, bar, stacked bar, and pie charts.

Every renderer has loading, empty, stale, invalid-data, and error states. A widget failure cannot break the page or other widgets.

### 5.5 Caching

Redis caches normalized widget results by definition version, parameters, dashboard filters, and data-source identity. Identical concurrent queries are deduplicated. Stale-while-revalidate may show the last successful value with a visible stale timestamp.

## 6. Layout model

A layout stores:

- name and stable id;
- viewport breakpoint;
- widget instance ids;
- grid coordinates and dimensions;
- per-instance widget parameters;
- visibility and refresh overrides;
- schema version and update time.

Desktop, tablet, and mobile arrangements are saved independently. Moving or resizing is optimistic in the UI and debounced to PostgreSQL. A failed save restores the last confirmed layout and shows an actionable error.

Import validates schema and widget references before applying. Unknown widget definitions are reported and skipped only after explicit confirmation.

## 7. Data model

Primary tables:

- `providers`;
- `secret_records`;
- `accounts`;
- `deployments`;
- `model_aliases`;
- `routing_policies` and `routing_tiers`;
- `virtual_keys`;
- `budgets` and `budget_states`;
- `usage_events` and partitioned `usage_rollups`;
- `pricing_versions`;
- `audit_events`;
- `settings`;
- `dashboard_layouts`;
- `widget_definitions` and `widget_instances`;
- `config_versions` and `gateway_config_acks`.

Foreign keys prevent routes from referencing missing accounts or models. Soft deletion is used where historical usage and audit records require stable references.

## 8. Credential security

- envelope encryption with a deployment master key;
- unique data-encryption key per secret record;
- authenticated encryption and key-version metadata;
- plaintext exists only during provider calls or validation;
- read APIs return secret presence and last rotation time, never plaintext;
- credentials are redacted from errors, logs, audit data, and snapshots exposed to the browser;
- gateway snapshots contain only the credentials needed by the gateway and are delivered through an authenticated private channel.

For local development, a file-backed master key is supported with restrictive permissions and a prominent warning. Production deployments should use a secret manager or KMS-compatible provider.

## 9. Snapshot publication flow

1. A management transaction writes the desired configuration and an audit event.
2. The snapshot compiler reads one consistent PostgreSQL transaction.
3. It resolves provider/account/model references and validates routes, limits, capabilities, and secrets.
4. The compiler produces an immutable snapshot with a monotonically increasing version and checksum.
5. The snapshot is stored encrypted at rest.
6. Redis publishes only the new version id and checksum.
7. Each Go gateway fetches the snapshot through the authenticated internal API.
8. The gateway validates schema, checksum, references, and required credentials.
9. On success, it atomically swaps the in-memory snapshot and writes an acknowledgement.
10. On failure, it retains the previous snapshot and reports the validation error to the dashboard.

The UI shows desired version, active version per gateway, acknowledgement time, and rollout errors. Rollback republishes a previous valid configuration as a new version.

## 10. Management API

The API is versioned under `/api/control/v1` and grouped by resource:

- providers and accounts;
- deployments, aliases, and routing policies;
- virtual keys and budgets;
- usage and costs;
- audit events;
- settings and diagnostics;
- layouts, widget definitions, widget execution, and widget validation;
- snapshots, publication, acknowledgements, and rollback;
- backup, restore, import, and export.

Mutations use optimistic concurrency with resource versions. Conflicts return the current server version and do not silently overwrite changes.

## 11. Error handling

- Field-level validation errors are returned in a stable problem-details format.
- Provider validation distinguishes authentication, quota, model, network, TLS, proxy, and timeout failures.
- Snapshot compile errors block publication but preserve the draft configuration.
- Snapshot rollout failure preserves the previous active gateway version.
- Widget errors remain isolated to one card.
- Usage ingestion uses bounded queues and records dropped-event counters rather than blocking requests.
- PostgreSQL or Redis unavailability marks the dashboard degraded while the gateway continues using its last valid snapshot.

## 12. UI behavior and visual system

- dark, technical, card-first interface;
- compact left navigation with clear active state;
- account and route state uses text plus icons, not color alone;
- dense tables are reserved for management and audit pages;
- destructive actions require confirmation and name matching for credentials, keys, and restore operations;
- secret values are never prefilled;
- keyboard-accessible drag handles, dialogs, menus, and tables;
- reduced-motion support;
- responsive desktop-first layouts with usable tablet/mobile fallback.

## 13. Testing and feedback loop

### Unit

- schema and model validation;
- snapshot compiler and rollback;
- encryption and redaction;
- budget calculations;
- widget schema and transforms;
- SQL restrictions;
- REST allowlist and SSRF protection;
- layout migrations.

### Integration

- PostgreSQL migrations and constraints;
- Redis notifications, caching, and deduplication;
- provider credential validation through fake upstreams;
- usage ingestion and rollups;
- management API optimistic concurrency;
- snapshot fetch, validation, acknowledgement, and rollback.

### UI and browser

- component states;
- keyboard accessibility;
- drag, resize, add/remove, save, switch, reset, import, and export layouts;
- account creation and credential rotation;
- route editing and snapshot publication;
- custom REST and SQL widget creation;
- stale and error widget states.

### Security

- SSRF and redirect bypass attempts;
- SQL parser bypass attempts;
- secret leakage in logs, errors, browser payloads, exports, and audit records;
- unauthorized access when remote binding is disabled;
- malicious widget schemas and oversized responses.

### End to end

Docker Compose starts PostgreSQL, Redis, control plane, fake providers, and the Go gateway. The test creates an account, alias, route, virtual key, budget, layout, and custom widget, publishes a snapshot, observes the gateway acknowledgement, sends an SSE completion request, records usage, and verifies the dashboard updates.

## 14. Delivery decomposition

### Phase A: Durable control plane

- Next.js application and management API;
- PostgreSQL schema and migrations;
- Redis connection;
- secret encryption;
- account/provider/model/route/key/settings CRUD;
- snapshot compile, publish, fetch, acknowledgement, and rollback;
- Go gateway snapshot client;
- Docker Compose migration.

### Phase B: Operational dashboard

- card-first Overview;
- system widgets;
- health and usage ingestion;
- costs, budgets, audit, and exports;
- named responsive layouts with drag-and-drop and resize.

### Phase C: Safe custom widgets

- declarative widget schema;
- REST/JSON executor and SSRF controls;
- read-only SQL executor and analytics views;
- transforms, renderers, cache, deduplication, import, and export;
- security and browser test suites.

Each phase receives its own implementation plan and commit sequence. Phase A must be workflow-validated before Phase B, and Phase B before Phase C.

## 15. Explicit non-goals

- arbitrary JavaScript or HTML widgets;
- arbitrary database connections or unrestricted SQL;
- public unauthenticated remote access;
- users, teams, roles, SSO, and multi-tenant isolation;
- payment processing and customer billing;
- multi-region consensus;
- Kubernetes operators;
- provider-authentication bypasses, cookie scraping, or MITM integrations.
