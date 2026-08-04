import { ok } from '@/lib/api';
export function GET() { return ok({ status: 'ok', service: 'papi-control-plane' }); }
