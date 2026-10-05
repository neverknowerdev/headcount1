
-- create "agent_skills" table
CREATE TABLE "public"."agent_skills" (
  "agent_id" integer NOT NULL,
  "skill_id" integer NOT NULL,
  PRIMARY KEY ("agent_id", "skill_id"),
  CONSTRAINT "fk_agent_skills_agent" FOREIGN KEY ("agent_id") REFERENCES "public"."agents" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "fk_agent_skills_skill" FOREIGN KEY ("skill_id") REFERENCES "public"."skills" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
