-- Create "proxy_request_logs" table
CREATE TABLE `proxy_request_logs` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `agent_id` integer NOT NULL,
  `provider_id` integer NOT NULL,
  `model` text NOT NULL,
  `prompt_tokens` integer NULL,
  `completion_tokens` integer NULL,
  `total_tokens` integer NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_proxy_request_logs_provider` FOREIGN KEY (`provider_id`) REFERENCES `llm_providers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_proxy_request_logs_agent` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
