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
  return NextResponse.json({ error: { code: 'internal_error', message: 'Internal server error' } }, { status: 500 });
}

export function requireInternal(req: Request, token: string) {
  const got = req.headers.get('authorization')?.replace(/^Bearer\s+/i, '') ?? '';
  if (!crypto.timingSafeEqual(digest(got), digest(token))) throw new ApiError(401, 'unauthorized', 'Invalid internal service token');
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
