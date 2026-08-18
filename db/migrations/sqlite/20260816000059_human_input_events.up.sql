ALTER TABLE `run_events` DROP COLUMN `__enum_guard_event_type`;
ALTER TABLE `run_events` ADD COLUMN `__enum_guard_event_type` integer NOT NULL DEFAULT 1
  CHECK (`event_type` IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished', 'human_input_requested', 'human_input_answered'));
