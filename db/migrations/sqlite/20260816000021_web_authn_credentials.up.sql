-- Create "web_authn_credentials" table
CREATE TABLE `web_authn_credentials` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NOT NULL,
  `credential_id` blob NOT NULL,
  `public_key` blob NOT NULL,
  `sign_count` integer NULL,
  `transports` text NULL,
  `aa_guid` blob NULL,
  `backup_eligible` numeric NULL,
  `backup_state` numeric NULL,
  `nickname` text NULL,
  `wrapped_dek` text NOT NULL,
  `prf_salt` blob NOT NULL,
  `last_used_at` datetime NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_web_authn_credentials_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_web_authn_credentials_credential_id" to table: "web_authn_credentials"
CREATE UNIQUE INDEX `idx_web_authn_credentials_credential_id` ON `web_authn_credentials` (`credential_id`);
-- Create index "idx_web_authn_credentials_user_id" to table: "web_authn_credentials"
CREATE INDEX `idx_web_authn_credentials_user_id` ON `web_authn_credentials` (`user_id`);
