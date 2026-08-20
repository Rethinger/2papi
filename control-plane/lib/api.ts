import { NextResponse } from 'next/server';
import crypto from 'node:crypto';
import { ZodError } from 'zod';
import { redactSecrets } from './crypto';

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: unknown) { super(message); }
}

export const ok = (data: unknown, status = 200) => NextResponse.json({ data }, { status });

export function problem(error: unknown) {
  if (error instanceof ZodError) return NextResponse.json({ error: { code: 'validation_failed', message: 'Request validation failed', issues: error.issues } }, { status: 400 });
  if (error instanceof ApiError) return NextResponse.json({ error: { code: error.code, message: error.message, details: redactSecrets(error.details) } }, { status: error.status });
  if (process.env.CODEX_TEST_MODE === 'true' || process.env.CODEX_TEST_MODE === '1') {
    const diagnostic = error instanceof Error
      ? { name: error.name, message: error.message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 300), stack: error.stack?.split('\n').slice(0, 8).join('\n') }
      : { type: typeof error };
    console.error('unhandled control-plane API error', diagnostic);
  }
  return NextResponse.json({ error: { code: 'internal_error', message: 'Internal server error' } }, { status: 500 });
}

export function requireInternal(req: Request, token: string) {
  const got = req.headers.get('authorization')?.replace(/^Bearer\s+/i, '') ?? '';
  if (!crypto.timingSafeEqual(digest(got), digest(token))) throw new ApiError(401, 'unauthorized', 'Invalid internal service token');
}

export function requireGatewayIdentity(req: Request, claimedGatewayId: string): string {
  const gatewayId = req.headers.get('x-gateway-id') ?? '';
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$/.test(gatewayId)) {
    throw new ApiError(400, 'gateway_identity_missing', 'Valid gateway identity header required');
  }
  if (gatewayId !== claimedGatewayId) {
    throw new ApiError(403, 'gateway_identity_mismatch', 'Gateway identity does not match request body');
  }
  return gatewayId;
}

function digest(value: string): Buffer { return crypto.createHash('sha256').update(value).digest(); }

export async function readJsonBounded<T>(req: Request, maxBytes: number): Promise<T> {
  const contentLength = req.headers.get('content-length');
  if (contentLength !== null) {
    const parsed = Number(contentLength);
    if (!Number.isFinite(parsed) || parsed < 0) throw new ApiError(400, 'invalid_content_length', 'Invalid content-length header');
    if (parsed > maxBytes) throw new ApiError(413, 'payload_too_large', 'Request body exceeds maximum size');
  }
  const chunks: Buffer[] = [];
  let size = 0;
  const reader = req.body?.getReader();
  if (reader) {
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        size += value.byteLength;
        if (size > maxBytes) {
          await reader.cancel();
          throw new ApiError(413, 'payload_too_large', 'Request body exceeds maximum size');
        }
        chunks.push(Buffer.from(value));
      }
    } finally {
      reader.releaseLock();
    }
  }
  const bytes = Buffer.concat(chunks, size);
  if (bytes.length === 0) return {} as T;
  try {
    return JSON.parse(bytes.toString('utf8')) as T;
  } catch {
    throw new ApiError(400, 'invalid_json', 'Request body is not valid JSON');
  }
}
