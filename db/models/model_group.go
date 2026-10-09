package models

import "time"

type ModelGroup struct {
	ID          int32  `json:"id" gorm:"primaryKey"`
	Name        string `json:"name" gorm:"not null"`
	Slug        string `json:"slug" gorm:"not null;uniqueIndex"`
	UserID      *int32 `json:"user_id" gorm:"index"`
	Description string `json:"description"`
	// Kind says which models the group routes between: language models or
	// System One models. A group never mixes the two.
	Kind    string             `json:"kind" gorm:"not null;default:'llm'"`
	Members []ModelGroupMember `json:"members" gorm:"foreignKey:GroupID"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
