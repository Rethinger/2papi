import { NextResponse } from 'next/server';
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
  const got = req.headers.get('authorization')?.replace(/^Bearer\s+/i, '');
  if (got !== token) throw new ApiError(401, 'unauthorized', 'Invalid internal service token');
}
