package models

type RunEventType string

const (
	RunEventTypeLifecycleStatus RunEventType = "run_status"
	RunEventTypeStatusReport    RunEventType = "status_report"
	RunEventTypeStatusRefresh   RunEventType = "status_report_request"
	RunEventTypeWorkerQuestion  RunEventType = "worker_question"
	RunEventTypeSessionMessage  RunEventType = "session_message"
	RunEventTypeSessionAnswer   RunEventType = "session_message_answer"
	RunEventTypeWorkerFinished  RunEventType = "worker_finished"
)
