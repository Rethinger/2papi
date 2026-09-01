-- G1 gap: virtual-key budgets may reset monthly instead of daily.
-- budget_duration: 'day' (default) | 'month' — the gateway's policy bucket
-- keys on the calendar window, so 'month' resets on the first of the month.
ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS budget_duration text NOT NULL DEFAULT 'day';