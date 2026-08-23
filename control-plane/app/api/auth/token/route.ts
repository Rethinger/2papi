import { tokenCore, cloudAuthDeps } from '../../../../lib/cloud-auth';

export const dynamic = 'force-dynamic';

export function POST(request: Request) {
  return tokenCore(request, cloudAuthDeps());
}
