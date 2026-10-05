-- Create "activity_logs" table
CREATE TABLE `activity_logs` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `action` text NOT NULL,
  `entity_id` integer NULL,
  `entity_type` text NULL,
  `details` text NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_activity_logs_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
