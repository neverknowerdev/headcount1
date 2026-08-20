ALTER TABLE "public"."tasks" ADD COLUMN "done_at" timestamptz NULL;
ALTER TABLE "public"."runs" ADD COLUMN "workspace_path" text NULL;
