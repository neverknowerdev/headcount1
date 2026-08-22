ALTER TABLE "public"."agents"
  DROP COLUMN IF EXISTS "worker_permissions",
  DROP COLUMN IF EXISTS "worker_allowed_mc_ps";
