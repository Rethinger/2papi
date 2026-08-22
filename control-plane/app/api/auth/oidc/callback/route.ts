import { oidcCallbackCore, ssoRouteDeps } from '../../../../../lib/sso-routes';

export const dynamic = 'force-dynamic';

export function GET(request: Request) {
  return oidcCallbackCore(request, ssoRouteDeps());
}
