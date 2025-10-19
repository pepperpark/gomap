package syncer

// EventType enumerates emitted sync events.
type EventType string

const (
	EventMailboxStart    EventType = "mailbox_start"
	EventMailboxProgress EventType = "mailbox_progress"
	EventMailboxDone     EventType = "mailbox_done"
)

// Event carries progress about a mailbox.
type Event struct {
	Type    EventType
	Mailbox string
	Total   int
	Done    int
	Err     error
}
