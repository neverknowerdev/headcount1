
-- create "activity_logs" table
CREATE TABLE "public"."activity_logs" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "action" text NOT NULL,
  "entity_id" integer NULL,
  "entity_type" text NULL,
  "details" text NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_activity_logs_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
