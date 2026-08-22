import { signupCore, cloudAuthDeps } from '../../../../lib/cloud-auth';

export const dynamic = 'force-dynamic';

export function POST(request: Request) {
  return signupCore(request, cloudAuthDeps());
}
