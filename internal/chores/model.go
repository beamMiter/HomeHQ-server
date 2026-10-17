package chores

import "time"

// Categories the database accepts (migration 00004).
const defaultCategory = "general"

func validCategory(c string) bool {
	switch c {
	case "general", "kitchen", "bathroom", "outdoor":
		return true
	}
	return false
}

type Chore struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Category    string     `json:"category"`
	Status      string     `json:"status"`
	Note        *string    `json:"note,omitempty"`
	ClaimedBy   *string    `json:"claimed_by,omitempty"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	CompletedBy *string    `json:"completed_by,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type CreateChoreReq struct {
	Title    string  `json:"title"`
	Category string  `json:"category"`
	Note     *string `json:"note,omitempty"`
}