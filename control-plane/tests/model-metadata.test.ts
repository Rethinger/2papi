import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeModelMetadata, normalizeModelMetadata, planIncompatibility } from '../lib/model-metadata.ts';

const EMPTY_EXTENDED = {
  tool_names: null,
  input_modalities: null,
  parallel_tool_calls: null,
  available_in_plans: null,
  last_seen_at: null,
};

test('normalizes Codex and API-provider metadata without model-name inference', () => {
  assert.deepEqual(normalizeModelMetadata({ context_window: 272000, supported_in_api: true }), {
    context_window: 272000, tools: null, function_calling: null, reasoning: null,
    image_generation: null,
    supported_in_api: true, tier: null, owner: null, description: null,
    ...EMPTY_EXTENDED,
  });
  assert.deepEqual(normalizeModelMetadata({ tool_call: true, function_call: true, blurb: 'Fast model', tier: 'low', owned_by: 'openrouter' }), {
    context_window: null, tools: true, function_calling: true, reasoning: null,
    image_generation: null,
    supported_in_api: null, tier: 'low', owner: 'openrouter', description: 'Fast model',
    ...EMPTY_EXTENDED,
  });
  assert.equal(normalizeModelMetadata({ capabilities: { tools: false, reasoning: true } }).reasoning, true);
  assert.deepEqual(normalizeModelMetadata({ id: 'gpt-reasoning-tools-1m' }), {
    context_window: null, tools: null, function_calling: null, reasoning: null,
    image_generation: null,
    supported_in_api: null, tier: null, owner: null, description: null,
    ...EMPTY_EXTENDED,
  });
});

test('merges account metadata deterministically', () => {
  assert.deepEqual(mergeModelMetadata([
    normalizeModelMetadata({ context_window: 128000, tool_call: false, function_call: false, blurb: 'First' }),
    normalizeModelMetadata({ context_window: 272000, tool_call: true, function_call: false, blurb: 'Second', owned_by: 'owner' }),
  ]), {
    context_window: 272000, tools: true, function_calling: false, reasoning: null,
    image_generation: null,
    supported_in_api: null, tier: null, owner: 'owner', description: 'First',
    ...EMPTY_EXTENDED,
  });
  assert.equal(mergeModelMetadata([normalizeModelMetadata({ tool_call: false }), normalizeModelMetadata({})]).tools, null);
});

test('extracts real tool names and modalities from Codex capabilities', () => {
  assert.deepEqual(normalizeModelMetadata({
    capabilities: { tools: true, web_search: true, code_execution: true, vision: true, prompt_caching: false },
    supports_parallel_tool_calls: true,
    input_modalities: ['text', 'image'],
    experimental_supported_tools: ['web_search', 'custom_tool'],
    last_seen_at: '2026-08-13T00:00:00Z',
  }), {
    context_window: null, tools: true, function_calling: null, reasoning: null,
    image_generation: null,
    supported_in_api: null, tier: null, owner: null, description: null,
    tool_names: ['web_search', 'code_execution', 'vision', 'custom_tool'],
    input_modalities: ['text', 'image'],
    parallel_tool_calls: true,
    available_in_plans: null,
    last_seen_at: '2026-08-13T00:00:00Z',
  });
  assert.deepEqual(normalizeModelMetadata({ web_search_tool_type: 'text_and_image', supports_search_tool: true }).tool_names, ['web_search']);
  assert.equal(normalizeModelMetadata({ capabilities: { web_search: false } }).tool_names, null);
  assert.deepEqual(normalizeModelMetadata({ input_modalities: ['audio'] }).tool_names, ['audio']);
});

test('plan compatibility flags accounts outside the model plan list', () => {
  const plans = new Map<string, unknown>([
    ['a1', 'plus'],
    ['a2', 'pro'],
    ['a3', null],
  ]);
  const metadata = normalizeModelMetadata({ available_in_plans: ['pro', 'team'] });
  assert.equal(planIncompatibility(metadata, ['a1'], plans), true);
  assert.equal(planIncompatibility(metadata, ['a2'], plans), false);
  assert.equal(planIncompatibility(metadata, ['a1', 'a2'], plans), false, 'one compatible account suffices');
  assert.equal(planIncompatibility(metadata, ['a3'], plans), false, 'unknown plan is not judged');
  assert.equal(planIncompatibility(metadata, [], plans), false);
  assert.equal(planIncompatibility(normalizeModelMetadata({}), ['a1'], plans), false, 'no plan list means no warning');
  assert.equal(planIncompatibility(null, ['a1'], plans), false);
  assert.equal(planIncompatibility(metadata, ['a1'], new Map()), false);
});

test('merges tool names, modalities, plans and staleness across accounts', () => {
  assert.deepEqual(mergeModelMetadata([
    normalizeModelMetadata({ capabilities: { web_search: true }, input_modalities: ['text'] }),
    normalizeModelMetadata({ capabilities: { code_execution: true }, input_modalities: ['text', 'image'], available_in_plans: ['plus', 'pro'], last_seen_at: '2026-08-12T00:00:00Z' }),
    normalizeModelMetadata({ last_seen_at: '2026-08-13T00:00:00Z', available_in_plans: ['team'] }),
  ]), {
    context_window: null, tools: null, function_calling: null, reasoning: null,
    image_generation: null,
    supported_in_api: null, tier: null, owner: null, description: null,
    tool_names: ['web_search', 'code_execution', 'vision'],
    input_modalities: ['text', 'image'],
    parallel_tool_calls: null,
    available_in_plans: ['plus', 'pro', 'team'],
    last_seen_at: '2026-08-13T00:00:00Z',
  });
});

test('normalizes and merges image generation capability', () => {
  assert.equal(normalizeModelMetadata({ capabilities: { image_generation: true } }).image_generation, true);
  assert.deepEqual(normalizeModelMetadata({ capabilities: { image_generation: true } }).tool_names, ['image_generation']);
  assert.equal(normalizeModelMetadata({ capabilities: { image_gen: true } }).image_generation, true);
  assert.equal(normalizeModelMetadata({ capabilities: { generate_image: true } }).image_generation, true);
  assert.equal(normalizeModelMetadata({ capabilities: { dalle_image_generation: true } }).image_generation, true);
  assert.equal(normalizeModelMetadata({ capabilities: { image_generation: false } }).image_generation, false);
  assert.equal(normalizeModelMetadata({}).image_generation, null);
  assert.equal(mergeModelMetadata([normalizeModelMetadata({}), normalizeModelMetadata({ capabilities: { image_generation: true } })]).image_generation, true);
  assert.equal(mergeModelMetadata([normalizeModelMetadata({ capabilities: { image_generation: false } }), normalizeModelMetadata({ capabilities: { image_generation: false } })]).image_generation, false);
  assert.equal(mergeModelMetadata([normalizeModelMetadata({ capabilities: { image_generation: false } }), normalizeModelMetadata({})]).image_generation, null);
});
