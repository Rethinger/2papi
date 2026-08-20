import { codexImportAuthBatchCore, codexRouteDeps } from '../../../../../../lib/codex/routes';

export async function POST(request: Request) {
  try {
    const body = await request.json().catch(() => ({}));
    const data = await codexImportAuthBatchCore(body, codexRouteDeps());
    return Response.json({ data }, { status: 201 });
  } catch (error) {
    const problem = (error instanceof Error ? error.message : String(error)).split('\n')[0];
    return Response.json({ error: { message: problem, code: 'codex_batch_import_failed' } }, { status: 400 });
  }
}

export const dynamic = 'force-dynamic';
