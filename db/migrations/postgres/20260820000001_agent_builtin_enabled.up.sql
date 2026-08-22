ALTER TABLE "public"."agents"
  ADD COLUMN "builtin" boolean NOT NULL DEFAULT false,
  ADD COLUMN "enabled" boolean NOT NULL DEFAULT true;
