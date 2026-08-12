import type { PoolClient } from 'pg';
import { ApiError } from './api';
import { audit, storeDraft } from './control';

export async function deleteModelRoute(client: PoolClient, modelId: string) {
  const current = await client.query('SELECT id,alias FROM model_aliases WHERE id=$1 FOR UPDATE', [modelId]);
  if (!current.rows[0]) throw new ApiError(404, 'not_found', 'Model not found');
  const alias = current.rows[0].alias as string;
  await client.query(`UPDATE virtual_keys SET models=ARRAY(SELECT model FROM unnest(models) model WHERE model<>$1) WHERE $1=ANY(models)`, [alias]);
  await client.query('DELETE FROM model_aliases WHERE id=$1', [modelId]);
  await audit(client, 'delete', 'model_alias', modelId, { alias });
  await storeDraft(client);
  return { id: modelId, alias, deleted: true as const };
}

export async function updateProviderModelStrategy(client: PoolClient, modelId: string, strategy: 'round_robin' | 'quota_failover') {
  if (strategy !== 'round_robin' && strategy !== 'quota_failover') throw new ApiError(400, 'invalid_model_strategy', 'Provider model strategy is invalid');
  const updated = await client.query(
    'UPDATE model_aliases SET routing_strategy=$2,updated_at=now() WHERE id=$1 AND provider_id IS NOT NULL RETURNING *',
    [modelId, strategy],
  );
  if (!updated.rows[0]) throw new ApiError(404, 'not_found', 'Provider model not found');
  await audit(client, 'update_strategy', 'model_alias', modelId, { routing_strategy: strategy });
  await storeDraft(client);
  return updated.rows[0];
}
