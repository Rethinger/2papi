import { z } from 'zod';
import { tx } from '@/lib/db';
import { audit, storeDraft } from '@/lib/control';
import { ApiError } from '@/lib/api';

const ModelBatchSchema = z.object({
  lines: z.array(z.string().min(1).max(500)).min(1).max(200),
  // Optional explicit account ids; default is every enabled account.
  accounts: z.array(z.string().uuid()).optional(),
  fallbacks: z.array(z.string()).default([]),
  input_per_mtok: z.number().nonnegative().default(0),
  output_per_mtok: z.number().nonnegative().default(0),
});

// Line formats: "alias=upstream_model" | "alias upstream_model" | "alias upstream_model acct1,acct2"
function parseLine(line: string): { alias: string; upstream: string; accountNames: string[] } | null {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith('#')) return null;
  const [left, right] = trimmed.includes('=') ? trimmed.split('=', 2) : trimmed.split(/\s+/, 2);
  const alias = left.trim();
  let upstream = (right ?? '').trim();
  let accountNames: string[] = [];
  if (right && right.includes(',')) {
    const parts = right.split(',');
    upstream = parts.shift()?.trim() ?? '';
    accountNames = parts.map(p => p.trim()).filter(Boolean);
  }
  if (!alias || !upstream || !/^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/.test(alias)) return null;
  return { alias, upstream, accountNames };
}

export async function POST(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const parsed = ModelBatchSchema.parse(body);
    const result = await tx(async client => {
      const existingAliases = new Set<string>((await client.query('SELECT lower(alias) a FROM model_aliases')).rows.map((row: any) => row.a as string));
      const accounts = (await client.query('SELECT id, name FROM accounts WHERE enabled ORDER BY name')).rows as Array<{ id: string; name: string }>;
      const accountByName = new Map(accounts.map(a => [a.name, a.id]));
      const allowed = new Set((parsed.accounts && parsed.accounts.length > 0 ? parsed.accounts : accounts.map(a => a.id)));

      const created: Array<{ alias: string; upstream_model: string }> = [];
      const skipped: string[] = [];
      for (const raw of parsed.lines) {
        const parsedLine = parseLine(raw);
        if (!parsedLine) { skipped.push(raw.trim()); continue; }
        const key = parsedLine.alias.toLowerCase();
        if (existingAliases.has(key)) { skipped.push(`${parsedLine.alias} (already exists)`); continue; }

        let accountIds: string[] = [];
        if (parsedLine.accountNames.length > 0) {
          for (const name of parsedLine.accountNames) {
            const id = accountByName.get(name);
            if (id && allowed.has(id)) accountIds.push(id);
          }
          if (accountIds.length === 0) { skipped.push(`${parsedLine.alias} (unknown accounts)`); continue; }
        } else {
          accountIds = accounts.filter(a => allowed.has(a.id)).map(a => a.id);
          if (accountIds.length === 0) { skipped.push(`${parsedLine.alias} (no eligible accounts)`); continue; }
        }

        const row = (await client.query('INSERT INTO model_aliases (alias,upstream_model,enabled,fallbacks) VALUES ($1,$2,true,$3) RETURNING id', [parsedLine.alias, parsedLine.upstream, parsed.fallbacks])).rows[0];
        existingAliases.add(key);
        await client.query('INSERT INTO model_pricing (model_alias_id,input_per_mtok,output_per_mtok) VALUES ($1,$2,$3)', [row.id, parsed.input_per_mtok, parsed.output_per_mtok]);
        for (let i = 0; i < accountIds.length; i++) {
          await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [row.id, accountIds[i], i]);
        }
        created.push({ alias: parsedLine.alias, upstream_model: parsedLine.upstream });
      }
      if (created.length === 0) throw new ApiError(400, 'no_models_imported', `No models could be imported${skipped.length ? ` (skipped: ${skipped.join(', ')})` : ''}`);
      await storeDraft(client);
      await audit(client, 'models_batch_import', 'model_alias', undefined, { count: created.length, skipped: skipped.slice(0, 20) });
      return { created, skipped: skipped.slice(0, 50) };
    });
    return Response.json({ data: result }, { status: 201 });
  } catch (error) {
    const message = error instanceof Error ? error.message.split('\n')[0] : String(error);
    return Response.json({ error: { message, code: 'batch_import_failed' } }, { status: 400 });
  }
}

export const dynamic = 'force-dynamic';
