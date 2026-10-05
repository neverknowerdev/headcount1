
-- create "task_relations" table
CREATE TABLE "public"."task_relations" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "source_task_id" integer NOT NULL,
  "target_task_id" integer NOT NULL,
  "kind" text NOT NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_task_relations_source_task" FOREIGN KEY ("source_task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_task_relations_target_task" FOREIGN KEY ("target_task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_task_relations_company_id" to table: "task_relations"
CREATE INDEX "idx_task_relations_company_id" ON "public"."task_relations" ("company_id");
-- create index "idx_task_relations_kind" to table: "task_relations"
CREATE INDEX "idx_task_relations_kind" ON "public"."task_relations" ("kind");
-- create index "idx_task_relations_source_task_id" to table: "task_relations"
CREATE INDEX "idx_task_relations_source_task_id" ON "public"."task_relations" ("source_task_id");
-- create index "idx_task_relations_target_task_id" to table: "task_relations"
CREATE INDEX "idx_task_relations_target_task_id" ON "public"."task_relations" ("target_task_id");
-- create index "idx_task_relations_unique" to table: "task_relations"
CREATE UNIQUE INDEX "idx_task_relations_unique" ON "public"."task_relations" ("source_task_id", "target_task_id", "kind");
