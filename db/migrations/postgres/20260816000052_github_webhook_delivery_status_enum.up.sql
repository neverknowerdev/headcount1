ALTER TABLE "public"."git_hub_webhook_deliveries"
  ADD CONSTRAINT "ck_git_hub_webhook_deliveries_status_enum" CHECK ("status" IN ('pending', 'processing', 'failed', 'completed'));
