-- Шаг 6 хребта (Cloud): email verification tokens for self-serve signup.
-- Tokens are stored hashed (sha256); plaintext lives only in the
-- verification link. Single-use: deleted on successful verification.

CREATE TABLE IF NOT EXISTS email_verification_tokens (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS email_verification_tokens_hash_uq ON email_verification_tokens (token_hash);
CREATE INDEX IF NOT EXISTS email_verification_tokens_user_idx ON email_verification_tokens (user_id);
