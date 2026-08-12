import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeModelMetadata, normalizeModelMetadata } from '../lib/model-metadata.ts';

test('normalizes Codex and API-provider metadata without model-name inference', () => {
  assert.deepEqual(normalizeModelMetadata({ context_window: 272000, supported_in_api: true }), {
    context_window: 272000, tools: null, function_calling: null, reasoning: null,
    supported_in_api: true, tier: null, owner: null, description: null,
  });
  assert.deepEqual(normalizeModelMetadata({ tool_call: true, function_call: true, blurb: 'Fast model', tier: 'low', owned_by: 'openrouter' }), {
    context_window: null, tools: true, function_calling: true, reasoning: null,
    supported_in_api: null, tier: 'low', owner: 'openrouter', description: 'Fast model',
  });
  assert.equal(normalizeModelMetadata({ capabilities: { tools: false, reasoning: true } }).reasoning, true);
  assert.deepEqual(normalizeModelMetadata({ id: 'gpt-reasoning-tools-1m' }), {
    context_window: null, tools: null, function_calling: null, reasoning: null,
    supported_in_api: null, tier: null, owner: null, description: null,
  });
});

test('merges account metadata deterministically', () => {
  assert.deepEqual(mergeModelMetadata([
    normalizeModelMetadata({ context_window: 128000, tool_call: false, function_call: false, blurb: 'First' }),
    normalizeModelMetadata({ context_window: 272000, tool_call: true, function_call: false, blurb: 'Second', owned_by: 'owner' }),
  ]), {
    context_window: 272000, tools: true, function_calling: false, reasoning: null,
    supported_in_api: null, tier: null, owner: 'owner', description: 'First',
  });
  assert.equal(mergeModelMetadata([normalizeModelMetadata({ tool_call: false }), normalizeModelMetadata({})]).tools, null);
});
