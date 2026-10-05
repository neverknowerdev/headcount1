package models

type TaskRelationView struct {
	RelationID int32            `json:"relation_id"`
	Task       TaskRelationTask `json:"task"`
}
