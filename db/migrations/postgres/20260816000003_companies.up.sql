
-- create "companies" table
CREATE TABLE "public"."companies" (
  "id" serial NOT NULL,
  "name" text NOT NULL,
  "short_name" text NOT NULL,
  "description" text NULL,
  "color" text NULL,
  "team_id" integer NULL,
  "user_id" integer NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_companies_team_id" to table: "companies"
CREATE INDEX "idx_companies_team_id" ON "public"."companies" ("team_id");
-- create index "idx_companies_user_id" to table: "companies"
CREATE INDEX "idx_companies_user_id" ON "public"."companies" ("user_id");
