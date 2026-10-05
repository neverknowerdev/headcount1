
-- create "teams" table
CREATE TABLE "public"."teams" (
  "id" serial NOT NULL,
  "name" text NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
