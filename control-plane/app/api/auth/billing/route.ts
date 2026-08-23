import { billingCore, cloudAuthDeps } from '../../../../lib/cloud-auth';

export const dynamic = 'force-dynamic';

export function GET(request: Request) {
  return billingCore(request, cloudAuthDeps());
}
