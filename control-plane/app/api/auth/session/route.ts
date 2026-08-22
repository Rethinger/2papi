import { logoutCore, meCore, cloudAuthDeps } from '../../../../lib/cloud-auth';

export const dynamic = 'force-dynamic';

export function POST(request: Request) {
  return logoutCore(request, cloudAuthDeps());
}

export function GET(request: Request) {
  return meCore(request, cloudAuthDeps());
}
