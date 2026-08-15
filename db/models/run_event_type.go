package models

type RunEventType string

const (
	RunEventTypeLifecycleStatus RunEventType = "run_status"
	RunEventTypeStatusReport    RunEventType = "status_report"
	RunEventTypeStatusRefresh   RunEventType = "status_report_request"
	RunEventTypeWorkerQuestion  RunEventType = "worker_question"
)
