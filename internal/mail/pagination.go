// Package mail — pagination.go implements cursor-based pagination for
// the thread list. 30 threads are loaded at a time; more are fetched
// on scroll demand.
package mail

import "time"

const DefaultPageSize = 30

// ThreadPage is the result of a paginated thread list query.
type ThreadPage struct {
	Threads    []Thread `json:"threads"`
	NextCursor string   `json:"next_cursor,omitempty"` // empty = no more pages
	Total      int      `json:"total"`                 // total thread count for the identity
}

// PageRequest specifies the parameters for a thread list request.
type PageRequest struct {
	IdentityID string // filter by identity; empty = all
	Cursor     string // ISO timestamp cursor (last_message_at of last item seen)
	Limit      int    // defaults to DefaultPageSize
	ShowRead   bool   // include read threads
	Archived   bool   // show archived threads
}

// CursorFromThread returns the opaque cursor string for a given thread.
// The cursor is the RFC 3339 representation of last_message_at.
func CursorFromThread(t *Thread) string {
	return t.LastMessageAt.UTC().Format(time.RFC3339Nano)
}

// CursorToTime parses a cursor back to a time.Time.
func CursorToTime(cursor string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, cursor)
}
