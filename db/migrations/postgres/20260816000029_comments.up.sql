
-- create "comments" table
CREATE TABLE "public"."comments" (
  "id" serial NOT NULL,
  "task_id" integer NOT NULL,
  "author_type" text NOT NULL,
  "author_id" integer NULL,
  "content" text NOT NULL,
  "comment_type" text NULL DEFAULT '',
  "run_id" integer NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_comments_task" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
