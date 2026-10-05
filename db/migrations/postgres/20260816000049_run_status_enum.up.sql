ALTER TABLE "public"."runs"
  ADD CONSTRAINT "ck_runs_status_enum" CHECK ("status" IN ('running', 'completed', 'failed', 'canceled', 'paused', 'recoverable_failed', 'stale', 'resuming', 'waiting', 'interrupted'));
