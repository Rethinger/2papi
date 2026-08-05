import { env } from '../env';

export const CODEX_CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann';
export const CODEX_REDIRECT_URI = 'http://localhost:1455/auth/callback';

const PROD_AUTH = 'https://auth.openai.com';
const PROD_CHATGPT = 'https://chatgpt.com';

function testMode() { return env.CODEX_TEST_MODE || process.env.CODEX_TEST_MODE === 'true' || process.env.CODEX_TEST_MODE === '1'; }
function origin(kind: 'auth' | 'chatgpt') {
  const override = kind === 'auth' ? (process.env.CODEX_AUTH_ORIGIN ?? env.CODEX_AUTH_ORIGIN) : (process.env.CODEX_CHATGPT_ORIGIN ?? env.CODEX_CHATGPT_ORIGIN);
  if (!override) return kind === 'auth' ? PROD_AUTH : PROD_CHATGPT;
  if (!testMode()) throw new Error('Codex origin overrides require CODEX_TEST_MODE=true');
  return override.replace(/\/$/, '');
}

export function codexAuthOrigin() { return origin('auth'); }
export function codexChatGPTOrigin() { return origin('chatgpt'); }

export function codexUrls() {
  const auth = codexAuthOrigin();
  return {
    authorize: `${auth}/oauth/authorize`,
    token: `${auth}/oauth/token`,
    jwks: `${auth}/.well-known/jwks.json`,
    deviceUserCode: `${auth}/api/accounts/deviceauth/usercode`,
    deviceToken: `${auth}/api/accounts/deviceauth/token`,
    deviceVerification: `${auth}/codex/device`,
  };
}

export const OPENAI_AUTHORIZE_URL = `${PROD_AUTH}/oauth/authorize`;
export const OPENAI_TOKEN_URL = `${PROD_AUTH}/oauth/token`;
export const OPENAI_JWKS_URL = `${PROD_AUTH}/.well-known/jwks.json`;
export const OPENAI_DEVICE_USER_CODE_URL = `${PROD_AUTH}/api/accounts/deviceauth/usercode`;
export const OPENAI_DEVICE_TOKEN_URL = `${PROD_AUTH}/api/accounts/deviceauth/token`;
export const OPENAI_DEVICE_VERIFICATION_URL = `${PROD_AUTH}/codex/device`;
