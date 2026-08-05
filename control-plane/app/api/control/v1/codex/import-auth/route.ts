import { codexImportAuthCore, codexRouteDeps } from '../../../../../../lib/codex/routes';

export async function POST(request: Request) {
  return codexImportAuthCore(request, codexRouteDeps());
}
