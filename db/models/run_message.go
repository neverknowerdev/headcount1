package models

// SessionMessage is the durable payload stored in RunEvent.Payload. The
// human-readable text remains available for logs while the versioned envelope
// leaves room for future control-plane fields.
type SessionMessage struct {
	SchemaVersion int    `json:"schema_version"`
	Message       string `json:"message"`
}

func NewSessionMessage(message string) SessionMessage {
	return SessionMessage{SchemaVersion: 1, Message: message}
}

type WorkerFinishedMessage struct {
	SchemaVersion int    `json:"schema_version"`
	WorkerRunID   int32  `json:"worker_run_id"`
	Status        string `json:"status"`
	Summary       string `json:"summary"`
	Details       string `json:"details,omitempty"`
}
