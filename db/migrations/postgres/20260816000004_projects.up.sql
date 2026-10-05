
-- create "projects" table
CREATE TABLE "public"."projects" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "name" text NOT NULL,
  "description" text NULL,
  "workspace_folder" text NULL,
  "repository_url" text NULL,
  "git_hub_repository_id" bigint NULL,
  "git_hub_installation_id" bigint NULL,
  "git_hub_default_branch" text NULL,
  "is_external" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_projects_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_projects_git_hub_installation_id" to table: "projects"
CREATE INDEX "idx_projects_git_hub_installation_id" ON "public"."projects" ("git_hub_installation_id");
-- create index "idx_projects_git_hub_repository_id" to table: "projects"
CREATE INDEX "idx_projects_git_hub_repository_id" ON "public"."projects" ("git_hub_repository_id");
