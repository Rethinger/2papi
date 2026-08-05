import { z } from 'zod';

const boolish = z.preprocess(v => v === true || v === 'true' || v === '1', z.boolean()).default(false);

const EnvSchema = z.object({
  DATABASE_URL: z.string().url().default('postgres://postgres:postgres@localhost:5432/papi_control'),
  REDIS_URL: z.string().url().default('redis://localhost:6379'),
  CONTROL_PLANE_MASTER_KEY: z.string().default(Buffer.alloc(32, 7).toString('base64')),
  INTERNAL_SERVICE_TOKEN: z.string().min(16).default('dev-internal-service-token'),
  GATEWAY_SHARED_SECRET: z.string().min(1).default('dev-secret-change-me'),
  CONTROL_PLANE_BIND_HOST: z.string().default('127.0.0.1'),
  CODEX_TEST_MODE: boolish,
  CODEX_AUTH_ORIGIN: z.string().url().optional(),
  CODEX_CHATGPT_ORIGIN: z.string().url().optional(),
  SNAPSHOT_POLL_INTERVAL_SECONDS: z.coerce.number().int().positive().default(30),
  GATEWAY_CAPABILITY_TTL_SECONDS: z.coerce.number().int().positive().optional(),
  MIN_ACTIVE_GATEWAYS: z.coerce.number().int().nonnegative().default(1),
});

export const env = EnvSchema.parse(process.env);

export function masterKeyBytes(): Buffer {
  const raw = env.CONTROL_PLANE_MASTER_KEY;
  const decoded = /^[0-9a-fA-F]{64}$/.test(raw) ? Buffer.from(raw, 'hex') : Buffer.from(raw, 'base64');
  if (decoded.length !== 32) throw new Error('CONTROL_PLANE_MASTER_KEY must decode to 32 bytes');
  return decoded;
}
