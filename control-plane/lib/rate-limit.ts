// In-memory fixed-window rate limiting for self-serve auth endpoints
// (signup/login/verify). Per-key counters with lazy expiry; no external
// dependencies so plain OSS deployments get brute-force protection too.
// Multi-instance deployments should front this with a shared limiter.

const buckets = new Map<string, { count: number; resetAt: number }>();

// allow() consumes one slot for key and returns true when under the limit.
// A non-positive limit denies everything ("endpoint closed") — conservative
// default for auth surfaces.
export function rateLimit(key: string, limit: number, windowMs: number, now = Date.now()): boolean {
  if (limit <= 0 || windowMs <= 0) return false;
  if (buckets.size > 50_000) {
    for (const [k, b] of buckets) {
      if (b.resetAt <= now) buckets.delete(k);
    }
    if (buckets.size > 50_000) buckets.clear(); // hard cap: shed state rather than grow unbounded
  }
  const bucket = buckets.get(key);
  if (!bucket || bucket.resetAt <= now) {
    buckets.set(key, { count: 1, resetAt: now + windowMs });
    return true;
  }
  if (bucket.count >= limit) return false;
  bucket.count += 1;
  return true;
}

// clientIp resolves the caller IP: first hop of X-Forwarded-For when the
// deployment sits behind a proxy, otherwise a stable local placeholder.
export function clientIp(req: Request): string {
  const fwd = req.headers.get('x-forwarded-for');
  if (fwd) {
    const first = fwd.split(',')[0].trim();
    if (first) return first;
  }
  return 'local';
}

export function clearRateLimitsForTests(): void {
  buckets.clear();
}
