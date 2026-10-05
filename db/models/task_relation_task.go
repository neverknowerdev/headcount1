package models

// TaskRelationTask is the compact task projection used by relation APIs and
// board summaries. It deliberately omits full task associations.
type TaskRelationTask struct {
	ID     int32  `json:"id"`
	RefKey string `json:"ref_key"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
