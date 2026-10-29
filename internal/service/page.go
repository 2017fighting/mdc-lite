package service

import "encoding/json"

// Page is the list envelope every collection endpoint uses: `{items, total, page,
// per_page}`. A single object is returned bare, and an empty list is `[]` — the two
// rules that keep the frontend from branching on "is it wrapped".
type Page[T any] struct {
	Items   []T `json:"items"`
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// decodeJSON reads one stored JSON column into v. An empty or unreadable value is
// left as the zero value with an error the caller decides what to do with: a task
// whose metadata_json was hand-edited must not take the detail page down with it.
func decodeJSON(raw string, v any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), v)
}
