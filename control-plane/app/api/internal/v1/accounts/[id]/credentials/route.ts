import { NextRequest } from 'next/server';
import { z } from 'zod';
import { ApiError, ok, problem, readJsonBounded, requireInternal } from '../../../../../../../lib/api';
import { encryptSecretJson } from '../../../../../../../lib/crypto';
import { tx } from '../../../../../../../lib/db';
import { env } from '../../../../../../../lib/env';
import { credentialDigestFromDeclarative } from '../../../../../../../lib/snapshots';

const CredentialSchema = z.object({
  kind: z.enum(['api_key', 'oauth']),
  api_key: z.string().min(1).optional(),
  access_token: z.string().min(1).optional(),
  refresh_token: z.string().min(1).optional(),
  id_token: z.string().min(1).optional(),
  expires_at: z.string().datetime().optional(),
  client_id: z.string().min(1).optional(),
  chatgpt_account_id: z.string().min(1).optional(),
  revision: z.number().int().positive().optional(),
}).strict().superRefine((credential, ctx) => {
  if (credential.kind === 'api_key' && !credential.api_key) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'api_key is required for api_key credentials', path: ['api_key'] });
  }
  if (credential.kind === 'oauth' && !credential.access_token) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'access_token is required for OAuth credentials', path: ['access_token'] });
  }
});

const BodySchema = z.object({
  expected_revision: z.number().int().positive(),
  credential: CredentialSchema,
}).strict();

export async function PUT(req: NextRequest, ctx: { params: Promise<{ id: string }> | { id: string } }) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const gatewayID = req.headers.get('x-gateway-id')?.trim();
    if (!gatewayID || !/^[A-Za-z0-9._:-]{1,128}$/.test(gatewayID)) {
      throw new ApiError(403, 'gateway_required', 'Credentials may only be updated by a gateway service');
    }
    const contentType = req.headers.get('content-type')?.split(';', 1)[0].trim().toLowerCase();
    if (contentType !== 'application/json') {
      throw new ApiError(415, 'unsupported_media_type', 'Content-Type must be application/json');
    }
    const { id } = await ctx.params;
    const body = BodySchema.parse(await readJsonBounded<unknown>(req, 256 * 1024));
    const result = await tx(async client => {
      const current = await client.query('SELECT credential_revision FROM accounts WHERE id=$1 FOR UPDATE', [id]);
      if (!current.rows[0]) throw new ApiError(404, 'account_not_found', 'Account not found');
      if (Number(current.rows[0].credential_revision) !== body.expected_revision) {
        throw new ApiError(409, 'credential_revision_conflict', 'Credential revision conflict');
      }

      const { revision: _wireRevision, ...credentialToStore } = body.credential;
      const encrypted = encryptSecretJson(credentialToStore);
      const secret = await client.query(`INSERT INTO secret_records
        (purpose,key_version,data_key_nonce,data_key_ciphertext,data_key_tag,secret_nonce,secret_ciphertext,secret_tag)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, [
        'account_credential',
        encrypted.key_version,
        Buffer.from(encrypted.data_key_nonce, 'base64'),
        Buffer.from(encrypted.data_key_ciphertext, 'base64'),
        Buffer.from(encrypted.data_key_tag, 'base64'),
        Buffer.from(encrypted.secret_nonce, 'base64'),
        Buffer.from(encrypted.secret_ciphertext, 'base64'),
        Buffer.from(encrypted.secret_tag, 'base64'),
      ]);
      const nextRevision = body.expected_revision + 1;
      await client.query(`UPDATE accounts SET
        secret_record_id=$1,
        credential_revision=$2,
        token_expires_at=$3,
        last_credential_refresh_at=now(),
        credential_persistence_status='persisted',
        external_account_id=COALESCE($4, external_account_id),
        updated_at=now()
        WHERE id=$5`, [
        secret.rows[0].id,
        nextRevision,
        body.credential.expires_at ?? null,
        body.credential.chatgpt_account_id ?? null,
        id,
      ]);
      await client.query(`INSERT INTO audit_events (actor, action, resource_type, resource_id, payload)
        VALUES ($1,$2,$3,$4,$5)`, [
        `gateway:${gatewayID}`,
        'credential.updated',
        'account',
        id,
        { credential_revision: nextRevision },
      ]);
      const revisions = await client.query('SELECT id,credential_revision FROM accounts WHERE enabled ORDER BY id');
      return {
        credential_revision: nextRevision,
        credential_digest: credentialDigestFromDeclarative({ accounts: revisions.rows }),
      };
    });
    return ok(result);
  } catch (error) {
    return problem(error);
  }
}
