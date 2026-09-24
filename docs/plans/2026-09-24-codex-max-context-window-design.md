# Codex OAuth max context window override

## Decision

For `codex-oauth`, use the `max_context_window` value returned by the Codex models endpoint as the provider's `ModelMeta.ContextWindow` when it is greater than zero. Keep the change limited to this provider; do not add configuration flags or alter other providers.

## Rationale

The Codex endpoint currently reports `context_window: 272000` and `max_context_window: 872000` for GPT-5.6 Luna/Terra. This is an intentional experimental change to test whether the larger Codex allowance is usable through Harness. If the backend returns context-overflow errors, the change can be reverted.

## Implementation

Parse `max_context_window` in the endpoint response, carry it through the visible-model intermediate value, and select it for `types.ModelMeta.ContextWindow` when present. Add a code NOTE documenting that this deliberately treats the endpoint's maximum as the active window and must be reverted if `prompt too long` errors appear.

Update the provider metadata test to cover the selected value. No request-body parameter is added because Codex treats these fields as model metadata/local budgeting rather than inference request fields.
