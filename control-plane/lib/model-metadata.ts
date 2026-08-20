export type ModelMetadata = {
  context_window: number | null;
  tools: boolean | null;
  function_calling: boolean | null;
  reasoning: boolean | null;
  image_generation: boolean | null;
  supported_in_api: boolean | null;
  tier: string | null;
  owner: string | null;
  description: string | null;
  tool_names: string[] | null;
  input_modalities: string[] | null;
  parallel_tool_calls: boolean | null;
  available_in_plans: string[] | null;
  last_seen_at: string | null;
};

const STRING_LIMIT = 1024;

// Capability keys (either top-level or inside `capabilities`) mapped to a
// stable tool name shown in the UI. Mirrors the shapes returned by
// chatgpt.com/backend-api/codex/models and the official model catalog.
const TOOL_ALIASES: Record<string, string> = {
  web_search: 'web_search',
  search: 'web_search',
  supports_search_tool: 'web_search',
  code_execution: 'code_execution',
  data_analysis: 'code_execution',
  vision: 'vision',
  image_input: 'vision',
  audio: 'audio',
  audio_input: 'audio',
  file_upload: 'file_upload',
  files: 'file_upload',
  photo_upload: 'photo_upload',
  prompt_caching: 'prompt_caching',
  image_generation: 'image_generation',
  image_gen: 'image_generation',
  generate_image: 'image_generation',
  dalle_image_generation: 'image_generation',
};

export function normalizeModelMetadata(input: unknown): ModelMetadata {
  const root = record(input);
  const capabilities = record(root.capabilities);
  const tools: string[] = [];
  const pushTool = (name: string) => {
    if (name && !tools.includes(name)) tools.push(name);
  };

  const explicitToolLists: string[] = [];
  for (const source of [root, capabilities]) {
    for (const [key, value] of Object.entries(source)) {
      const mapped = TOOL_ALIASES[key];
      if (mapped && typeof value === 'boolean' && value) pushTool(mapped);
    }
    if (typeof source.web_search_tool_type === 'string' && source.web_search_tool_type.trim()) pushTool('web_search');
    if (Array.isArray(source.experimental_supported_tools)) {
      for (const item of source.experimental_supported_tools) {
        if (typeof item === 'string' && item.trim()) explicitToolLists.push(item.trim().slice(0, STRING_LIMIT));
      }
    }
  }
  for (const item of explicitToolLists) pushTool(item);

  const modalities = firstStringArray(root.input_modalities, root.modalities, capabilities.input_modalities);
  if (modalities) {
    if (modalities.includes('image')) pushTool('vision');
    if (modalities.includes('audio')) pushTool('audio');
  }

  return {
    context_window: firstPositiveNumber(root.context_window, root.context_length, root.max_context_tokens, capabilities.context_window, capabilities.context_length),
    tools: firstBoolean(root.tool_call, root.tools, root.supports_tools, capabilities.tools, capabilities.tool_call),
    function_calling: firstBoolean(root.function_call, root.function_calling, root.supports_function_calling, capabilities.function_calling, capabilities.function_call),
    reasoning: firstBoolean(root.reasoning, root.supports_reasoning, capabilities.reasoning),
    image_generation: firstBoolean(root.image_generation, capabilities.image_generation, root.image_gen, capabilities.image_gen, root.generate_image, capabilities.generate_image, root.dalle_image_generation, capabilities.dalle_image_generation),
    supported_in_api: firstBoolean(root.supported_in_api, capabilities.supported_in_api),
    tier: firstString(root.tier, capabilities.tier),
    owner: firstString(root.owned_by, root.owner, capabilities.owner),
    description: firstString(root.description, root.blurb, capabilities.description),
    tool_names: tools.length ? tools : null,
    input_modalities: modalities,
    parallel_tool_calls: firstBoolean(root.supports_parallel_tool_calls, root.parallel_tool_calls, capabilities.supports_parallel_tool_calls, capabilities.parallel_tool_calls),
    available_in_plans: firstStringArray(root.available_in_plans, capabilities.available_in_plans),
    last_seen_at: firstString(root.last_seen_at, capabilities.last_seen_at),
  };
}

export function planIncompatibility(metadata: ModelMetadata | null | undefined, accountIds: string[], planByAccount: ReadonlyMap<string, unknown>): boolean {
  const plans = metadata?.available_in_plans;
  if (!plans || plans.length === 0 || accountIds.length === 0) return false;
  const known = accountIds
    .map(id => planByAccount.get(id))
    .filter((plan): plan is string => typeof plan === 'string' && plan.trim().length > 0);
  if (known.length === 0) return false;
  const accepted = new Set(plans.map(plan => plan.toLowerCase()));
  return known.every(plan => !accepted.has(plan.toLowerCase()));
}

export function mergeModelMetadata(items: ModelMetadata[]): ModelMetadata {
  return {
    context_window: maxNumber(items.map(item => item.context_window)),
    tools: mergeBoolean(items.map(item => item.tools)),
    function_calling: mergeBoolean(items.map(item => item.function_calling)),
    reasoning: mergeBoolean(items.map(item => item.reasoning)),
    image_generation: mergeBoolean(items.map(item => item.image_generation)),
    supported_in_api: mergeBoolean(items.map(item => item.supported_in_api)),
    tier: firstPresent(items.map(item => item.tier)),
    owner: firstPresent(items.map(item => item.owner)),
    description: firstPresent(items.map(item => item.description)),
    tool_names: mergeStringArray(items.map(item => item.tool_names)),
    input_modalities: mergeStringArray(items.map(item => item.input_modalities)),
    parallel_tool_calls: mergeBoolean(items.map(item => item.parallel_tool_calls)),
    available_in_plans: mergeStringArray(items.map(item => item.available_in_plans)),
    last_seen_at: latestString(items.map(item => item.last_seen_at)),
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

function firstStringArray(...values: unknown[]) {
  for (const value of values) {
    if (!Array.isArray(value)) continue;
    const items = value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0).map(item => item.trim().slice(0, STRING_LIMIT));
    if (items.length) return items;
  }
  return null;
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

function mergeStringArray(groups: Array<string[] | null>) {
  const out: string[] = [];
  for (const group of groups) {
    if (!group) continue;
    for (const item of group) {
      if (!out.includes(item)) out.push(item);
    }
  }
  return out.length ? out : null;
}

function latestString(values: Array<string | null>) {
  const dated = values.filter((value): value is string => value !== null && Number.isFinite(Date.parse(value)));
  if (!dated.length) return firstPresent(values);
  return dated.reduce((best, value) => (Date.parse(value) > Date.parse(best) ? value : best));
}
