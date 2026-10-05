
-- create "skills" table
CREATE TABLE "public"."skills" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "name" text NOT NULL,
  "description" text NULL,
  "source_url" text NULL,
  "local_path" text NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_skills_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
