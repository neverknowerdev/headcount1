package models

type CheckpointPhase string

const (
	CheckpointPhaseBeforeTools CheckpointPhase = "before_tools"
	CheckpointPhaseAfterTools  CheckpointPhase = "after_tools"
)
