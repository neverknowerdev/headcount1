-- Create "agent_skills" table
CREATE TABLE `agent_skills` (
  `agent_id` integer NULL,
  `skill_id` integer NULL,
  PRIMARY KEY (`agent_id`, `skill_id`),
  CONSTRAINT `fk_agent_skills_skill` FOREIGN KEY (`skill_id`) REFERENCES `skills` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `fk_agent_skills_agent` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
