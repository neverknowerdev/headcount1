package models

type TaskRelationSummary struct {
	DependsOn []TaskRelationTask `json:"depends_on"`
	BlockedBy []TaskRelationTask `json:"blocked_by"`
	Blocks    []TaskRelationTask `json:"blocks"`
	RelatedTo []TaskRelationTask `json:"related_to"`
}
