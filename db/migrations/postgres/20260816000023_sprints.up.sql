
-- create "sprints" table
CREATE TABLE "public"."sprints" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "name" text NOT NULL,
  "goal" text NULL,
  "start_date" timestamptz NULL,
  "end_date" timestamptz NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_sprints_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
