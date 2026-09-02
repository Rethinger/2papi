import { Pool, PoolClient } from 'pg';
import { env } from './env';

const globalForPg = globalThis as unknown as { pgPool?: Pool };
let productionPool: Pool | undefined;

// The pool is built on first use, never at import time. ESM hoists `import`
// above every assignment, so a module that eagerly called `new Pool()` would
// bind to env.DATABASE_URL's dev default before a caller (integration tests,
// scripts) had a chance to point DATABASE_URL at its own database — queries
// then hit an unmigrated schema and fail with `relation "..." does not exist`.
function acquire(): Pool {
  const existing = globalForPg.pgPool ?? productionPool;
  if (existing) return existing;
  const created = new Pool({ connectionString: process.env.DATABASE_URL ?? env.DATABASE_URL, max: 10 });
  if (process.env.NODE_ENV !== 'production') globalForPg.pgPool = created;
  else productionPool = created;
  return created;
}

// Proxy keeps the historical `import { pool }` value shape (21 call sites plus
// duck-typed `'connect' in db` checks) while deferring construction.
export const pool = new Proxy({} as Pool, {
  get(_target, property) {
    const real = acquire();
    const value = Reflect.get(real, property, real);
    return typeof value === 'function' ? value.bind(real) : value;
  },
  set(_target, property, value) {
    return Reflect.set(acquire(), property, value);
  },
  has(_target, property) {
    return Reflect.has(acquire(), property);
  },
  getPrototypeOf() {
    return Reflect.getPrototypeOf(acquire());
  },
});

export async function tx<T>(fn: (client: PoolClient) => Promise<T>): Promise<T> {
  return txOn(pool, client => fn(client as PoolClient));
}

// Runs `fn` inside a real transaction on either a Pool or an already
// checked-out client. A Pool must hand out one dedicated connection first:
// `pool.query('BEGIN')` borrows an arbitrary connection and returns it to the
// pool, so the statements that follow can land on *other* connections and
// autocommit independently, while the connection holding the open transaction
// is left idle. The transaction reads as atomic but guarantees nothing.
export async function txOn<T>(database: Queryable, fn: (client: Queryable) => Promise<T>): Promise<T> {
  const borrowed = 'connect' in database && typeof (database as Pool).connect === 'function';
  const client: Queryable = borrowed ? await (database as Pool).connect() : database;
  try {
    await client.query('BEGIN');
    const out = await fn(client);
    await client.query('COMMIT');
    return out;
  } catch (error) {
    await client.query('ROLLBACK');
    throw error;
  } finally {
    if (borrowed) (client as PoolClient).release();
  }
}

export type Queryable = Pool | PoolClient;
