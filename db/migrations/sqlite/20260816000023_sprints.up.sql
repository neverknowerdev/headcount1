-- Create "sprints" table
CREATE TABLE `sprints` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `name` text NOT NULL,
  `goal` text NULL,
  `start_date` datetime NULL,
  `end_date` datetime NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_sprints_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
