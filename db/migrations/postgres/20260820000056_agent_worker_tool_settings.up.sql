ALTER TABLE "public"."agents"
  ADD COLUMN "worker_permissions" text NOT NULL DEFAULT '',
  ADD COLUMN "worker_allowed_mc_ps" text NOT NULL DEFAULT '';
