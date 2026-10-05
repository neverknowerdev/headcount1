
-- create "artifacts" table
CREATE TABLE "public"."artifacts" (
  "id" serial NOT NULL,
  "company_id" integer NULL,
  "project_id" integer NULL,
  "task_id" integer NOT NULL,
  "run_id" integer NOT NULL,
  "filename" text NOT NULL,
  "file_path" text NOT NULL,
  "content" text NULL,
  "description" text NULL DEFAULT '',
  "is_verified" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_artifacts_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_artifacts_project" FOREIGN KEY ("project_id") REFERENCES "public"."projects" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_artifacts_run" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_artifacts_task" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_artifacts_company_id" to table: "artifacts"
CREATE INDEX "idx_artifacts_company_id" ON "public"."artifacts" ("company_id");
-- create index "idx_artifacts_project_id" to table: "artifacts"
CREATE INDEX "idx_artifacts_project_id" ON "public"."artifacts" ("project_id");
