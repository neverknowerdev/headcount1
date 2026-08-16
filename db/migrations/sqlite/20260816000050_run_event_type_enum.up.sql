ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` INTEGER NOT NULL DEFAULT 1 CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
