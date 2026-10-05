-- Create "skills" table
CREATE TABLE `skills` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `source_url` text NULL,
  `local_path` text NOT NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_skills_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
