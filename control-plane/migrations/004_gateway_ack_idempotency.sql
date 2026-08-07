WITH ranked AS (
  SELECT id,
    row_number() OVER (
      PARTITION BY
        gateway_id,
        version,
        status,
        schema_version,
        COALESCE(config_checksum, checksum),
        COALESCE(credential_digest, ''),
        COALESCE(runtime_checksum, checksum),
        envelope_version
      ORDER BY acknowledged_at DESC, id DESC
    ) AS duplicate_rank
  FROM gateway_config_acks
)
DELETE FROM gateway_config_acks
WHERE id IN (SELECT id FROM ranked WHERE duplicate_rank > 1);

CREATE UNIQUE INDEX IF NOT EXISTS gateway_config_acks_identity_uq
ON gateway_config_acks (
  gateway_id,
  version,
  status,
  schema_version,
  (COALESCE(config_checksum, checksum)),
  (COALESCE(credential_digest, '')),
  (COALESCE(runtime_checksum, checksum)),
  envelope_version
);
