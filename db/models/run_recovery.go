package models

import "time"

type RunRecovery struct {
	CheckpointSequence   int64           `json:"checkpoint_sequence,omitempty"`
	CheckpointVersion    int             `json:"checkpoint_version,omitempty"`
	CheckpointPhase      CheckpointPhase `json:"checkpoint_phase,omitempty"`
	RecoveryReason       string          `json:"recovery_reason,omitempty"`
	RecoveryInitiator    string          `json:"recovery_initiator,omitempty"`
	RecoveryTarget       string          `json:"recovery_target,omitempty"`
	ResumeLeaseOwner     string          `json:"resume_lease_owner,omitempty"`
	ResumeLeaseUntil     *time.Time      `json:"resume_lease_until,omitempty"`
	ResumePreviousStatus string          `json:"resume_previous_status,omitempty"`
	ResumeAttempts       int             `json:"resume_attempts,omitempty"`
	LastResumeError      string          `json:"last_resume_error,omitempty"`
	WaitReason           string          `json:"wait_reason,omitempty"`
	WaitCommentID        int32           `json:"wait_comment_id,omitempty"`
	StopCause            string          `json:"stop_cause,omitempty"`
	RecoveryAttempts     int             `json:"recovery_attempts,omitempty"`
}
