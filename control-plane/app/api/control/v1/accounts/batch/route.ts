import { z } from 'zod';
import { tx } from '@/lib/db';
import { audit, insertSecret, storeDraft } from '@/lib/control';
import { ApiError } from '@/lib/api';

const AccountBatchSchema = z.object({
  provider_id: z.string().uuid(),
  kind: z.enum(['api_key', 'cookie', 'oauth']).default('api_key'),
  entries: z.array(z.object({ name: z.string().min(1).max(120).optional(), secret: z.string().min(1).max(4096) })).min(1).max(500),
  priority: z.number().int().default(1),
  max_concurrency: z.number().int().positive().default(100),
});

export async function POST(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const parsed = AccountBatchSchema.parse(body);
    const result = await tx(async client => {
      const provider = (await client.query('SELECT * FROM providers WHERE id=$1', [parsed.provider_id])).rows[0];
      if (!provider) throw new ApiError(404, 'provider_not_found', 'Provider not found');

      const used = new Set<string>((await client.query('SELECT name FROM accounts')).rows.map((row: any) => row.name as string));
      const created: Array<{ id: string; name: string }> = [];
      for (const entry of parsed.entries) {
        const base = entry.name?.trim() || `${provider.slug}-${created.length + 1}`;
        let name = base;
        let suffix = 2;
        while (used.has(name)) { name = `${base}-${suffix++}`; }
        used.add(name);

        const credential = parsed.kind === 'cookie'
          ? { kind: 'cookie', cookies: entry.secret }
          : parsed.kind === 'oauth'
            ? { kind: 'oauth', access_token: entry.secret }
            : { kind: 'api_key', api_key: entry.secret };
        const secretId = await insertSecret(client, 'account_credential', credential);
        const displayName = entry.name?.trim() || `${provider.name} #${created.length + 1}`;
        const row = (await client.query(`INSERT INTO accounts
          (provider_id, secret_record_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost, credential_revision, metadata)
          VALUES ($1,$2,$3,$4,$5,true,$6,1,$7,0,1,$8) RETURNING id`, [
          parsed.provider_id, secretId, name, displayName, provider.base_url,
          parsed.priority, parsed.max_concurrency, JSON.stringify({ auth_method: parsed.kind, batch: true }),
        ])).rows[0];
        created.push({ id: String(row.id), name });
      }
      await storeDraft(client);
      await audit(client, 'accounts_batch_import', 'provider', parsed.provider_id, { count: created.length, kind: parsed.kind });
      return created;
    });
    return Response.json({ data: result }, { status: 201 });
  } catch (error) {
    const problem = (error instanceof Error ? error.message : String(error)).split('\n')[0];
    return Response.json({ error: { message: problem, code: 'batch_import_failed' } }, { status: 400 });
  }
}

export const dynamic = 'force-dynamic';
