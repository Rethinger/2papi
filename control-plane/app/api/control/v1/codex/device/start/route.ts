import { codexDeviceStartCore, codexRouteDeps } from '../../../../../../../lib/codex/routes';

export async function POST(request: Request) {
  return codexDeviceStartCore(request, codexRouteDeps());
}
