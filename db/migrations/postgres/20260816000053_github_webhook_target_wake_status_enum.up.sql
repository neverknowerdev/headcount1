ALTER TABLE "public"."git_hub_webhook_targets"
  ADD CONSTRAINT "ck_git_hub_webhook_targets_wake_status_enum" CHECK ("wake_status" IN ('pending', 'processing', 'completed'));
