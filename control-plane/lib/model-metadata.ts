export type ModelMetadata = {
  context_window: number | null;
  tools: boolean | null;
  function_calling: boolean | null;
  reasoning: boolean | null;
  supported_in_api: boolean | null;
  tier: string | null;
  owner: string | null;
  description: string | null;
};

const STRING_LIMIT = 1024;

export function normalizeModelMetadata(input: unknown): ModelMetadata {
  const root = record(input);
  const capabilities = record(root.capabilities);
  return {
    context_window: firstPositiveNumber(root.context_window, root.context_length, root.max_context_tokens, capabilities.context_window, capabilities.context_length),
    tools: firstBoolean(root.tool_call, root.tools, root.supports_tools, capabilities.tools, capabilities.tool_call),
    function_calling: firstBoolean(root.function_call, root.function_calling, root.supports_function_calling, capabilities.function_calling, capabilities.function_call),
    reasoning: firstBoolean(root.reasoning, root.supports_reasoning, capabilities.reasoning),
    supported_in_api: firstBoolean(root.supported_in_api, capabilities.supported_in_api),
    tier: firstString(root.tier, capabilities.tier),
    owner: firstString(root.owned_by, root.owner, capabilities.owner),
    description: firstString(root.description, root.blurb, capabilities.description),
  };
}

export function mergeModelMetadata(items: ModelMetadata[]): ModelMetadata {
  return {
    context_window: maxNumber(items.map(item => item.context_window)),
    tools: mergeBoolean(items.map(item => item.tools)),
    function_calling: mergeBoolean(items.map(item => item.function_calling)),
    reasoning: mergeBoolean(items.map(item => item.reasoning)),
    supported_in_api: mergeBoolean(items.map(item => item.supported_in_api)),
    tier: firstPresent(items.map(item => item.tier)),
    owner: firstPresent(items.map(item => item.owner)),
    description: firstPresent(items.map(item => item.description)),
  };
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function firstPositiveNumber(...values: unknown[]) {
  for (const value of values) {
    const number = typeof value === 'string' && value.trim() ? Number(value) : value;
    if (typeof number === 'number' && Number.isFinite(number) && number > 0) return Math.floor(number);
  }
  return null;
}

function firstBoolean(...values: unknown[]) {
  return values.find((value): value is boolean => typeof value === 'boolean') ?? null;
}

function firstString(...values: unknown[]) {
  const value = values.find((candidate): candidate is string => typeof candidate === 'string' && candidate.trim().length > 0);
  return value ? value.trim().slice(0, STRING_LIMIT) : null;
}

function maxNumber(values: Array<number | null>) {
  const known = values.filter((value): value is number => value !== null);
  return known.length ? Math.max(...known) : null;
}

function mergeBoolean(values: Array<boolean | null>) {
  if (values.some(value => value === true)) return true;
  return values.length > 0 && values.every(value => value === false) ? false : null;
}

function firstPresent(values: Array<string | null>) {
  return values.find((value): value is string => value !== null) ?? null;
}
