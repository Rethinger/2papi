# docs/history — design records from before `.kiro/specs`

These are the original design documents and implementation plans for the first
phases of 2papi, written between 2026-08-04 and 2026-08-12: the gateway MVP,
the dashboard control plane, the Codex provider, dashboard localization and
provider model pools. They describe what was decided and why, and several of
them are still the only written account of a subsystem's shape.

They are **history, not process.** Current work uses spec folders under
[`.kiro/specs/`](../../.kiro/specs/) — `requirements.md` / `design.md` /
`tasks.md` per feature. Nothing here is maintained against the code; where a
document and the code disagree, the code wins. Start from
[the README](../../README.md) and [`docs/`](..) for how the system works today.

Directory was previously `docs/superpowers/`, after the tooling that generated
it; renamed because the tool name said nothing about the contents.

| Plans | Designs |
|---|---|
| `plans/2026-08-04-multi-account-ai-gateway-phases-1-2.md` | `specs/2026-08-04-multi-account-ai-gateway-design.md` |
| `plans/2026-08-04-dashboard-control-plane-phase-a.md` | `specs/2026-08-04-dashboard-control-plane-design.md` |
| `plans/2026-08-05-openai-codex-provider.md` | `specs/2026-08-05-openai-codex-provider-design.md` |
| `plans/2026-08-05-dashboard-ru-en-localization.md` | `specs/2026-08-05-dashboard-ru-en-localization-design.md` |
| `plans/2026-08-12-provider-model-pools.md` | `specs/2026-08-12-provider-model-pools-design.md` |
