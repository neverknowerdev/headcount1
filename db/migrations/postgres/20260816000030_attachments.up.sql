
-- create "attachments" table
CREATE TABLE "public"."attachments" (
  "id" serial NOT NULL,
  "task_id" integer NOT NULL,
  "comment_id" integer NULL,
  "filename" text NOT NULL,
  "file_path" text NOT NULL,
  "mime_type" text NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_attachments_comment" FOREIGN KEY ("comment_id") REFERENCES "public"."comments" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_attachments_task" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
