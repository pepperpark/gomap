package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yourname/gomap/internal/imaputil"
	"github.com/yourname/gomap/internal/state"
)

// no init needed

type Options struct {
	DryRun      bool
	Since       time.Time
	Concurrency int
	Quiet       bool
	Map         map[string]string // optional exact mailbox name mapping: src->dst
	IgnoreState bool              // if true, do not use resume state (start from UID 0)
}

type MailboxSyncer struct {
	src, dst *client.Client
	st       *state.State
	opts     Options
	events   chan Event
}

func NewMailboxSyncer(src, dst *client.Client, st *state.State, opts Options) *MailboxSyncer {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	return &MailboxSyncer{src: src, dst: dst, st: st, opts: opts, events: make(chan Event, 128)}
}

func (m *MailboxSyncer) SyncAll(ctx context.Context, mailboxes []string) []error {
	sem := make(chan struct{}, m.opts.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := []error{}

	// On cancel, force-close IMAP connections to unblock I/O
	go func() {
		<-ctx.Done()
		// Best-effort: ignore errors; this should unblock ongoing operations
		_ = m.src.Logout()
		_ = m.dst.Logout()
	}()
	for _, box := range mailboxes {
		box := box
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.syncMailbox(ctx, box); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", box, err))
				mu.Unlock()
			}
			<-sem
		}()
	}
	wg.Wait()
	close(m.events)
	return errs
}

func (m *MailboxSyncer) syncMailbox(ctx context.Context, name string) error {
	if !m.opts.Quiet {
		log.Printf("[mailbox] %s: start", name)
	}
	m.emit(Event{Type: EventMailboxStart, Mailbox: name})
	// Ensure destination mailbox exists
	if !m.opts.DryRun {
		if err := m.ensureDstMailbox(name); err != nil {
			return err
		}
	}
	// Select source mailbox
	if _, err := imaputil.SelectMailbox(m.src, name, true); err != nil {
		return err
	}
	var minUID uint32
	if !m.opts.IgnoreState {
		minUID = m.st.GetMaxUID(name)
	}
	uids, err := imaputil.SearchUIDsSince(m.src, m.opts.Since, minUID)
	if err != nil {
		return err
	}
	if len(uids) == 0 {
		if !m.opts.Quiet {
			log.Printf("[mailbox] %s: no new messages", name)
		}
		return nil
	}
	if !m.opts.Quiet {
		log.Printf("[mailbox] %s: copying %d messages (from UID>%d)", name, len(uids), minUID)
	}
	m.emit(Event{Type: EventMailboxProgress, Mailbox: name, Total: len(uids), Done: 0})

	seq := new(imap.SeqSet)
	for _, uid := range uids {
		seq.AddNum(uid)
	}

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{section.FetchItem(), imap.FetchInternalDate, imap.FetchFlags, imap.FetchUid}
	msgs := make(chan *imap.Message, 64)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- m.src.UidFetch(seq, items, msgs)
	}()
	done := 0
	fetchErr := error(nil)
	msgsClosed := false
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				msgsClosed = true
				// if fetch already errored, return it
				if fetchErr != nil {
					return fetchErr
				}
				// otherwise we are done reading all messages
				m.emit(Event{Type: EventMailboxDone, Mailbox: name})
				return nil
			}
			if msg == nil {
				continue
			}
			uid := msg.Uid
			date := msg.InternalDate
			flags := msg.Flags
			lit := msg.GetBody(section)
			if lit == nil {
				if !m.opts.Quiet {
					log.Printf("[mailbox] %s: UID %d has no body, skipped", name, uid)
				}
				continue
			}
			if m.opts.DryRun {
				if !m.opts.Quiet {
					log.Printf("[dry-run] append %s UID %d flags=%v date=%s", name, uid, flags, date)
				}
				done++
				m.emit(Event{Type: EventMailboxProgress, Mailbox: name, Total: len(uids), Done: done})
				continue
			}
			if err := m.appendToDst(name, lit, date, flags); err != nil {
				return err
			}
			m.st.SetMaxUID(name, uid)
			done++
			m.emit(Event{Type: EventMailboxProgress, Mailbox: name, Total: len(uids), Done: done})
		case err := <-doneCh:
			// record fetch completion (and possible error) but continue draining msgs
			if err != nil {
				fetchErr = err
				// if messages channel already closed, return immediately
				if msgsClosed {
					return fetchErr
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *MailboxSyncer) ensureDstMailbox(name string) error {
	dstName := m.mapName(name)
	_, err := imaputil.SelectMailbox(m.dst, dstName, false)
	if err == nil {
		return nil
	}
	// Try to create
	if err := m.dst.Create(dstName); err != nil {
		// If already exists or cannot create, attempt select again
		if _, selErr := imaputil.SelectMailbox(m.dst, dstName, false); selErr == nil {
			return nil
		}
		return fmt.Errorf("create mailbox %s: %w", dstName, err)
	}
	return nil
}

func (m *MailboxSyncer) appendToDst(name string, r imap.Literal, date time.Time, flags []string) error {
	// Ensure mailbox selected RW
	dstName := m.mapName(name)
	if _, err := imaputil.SelectMailbox(m.dst, dstName, false); err != nil {
		return err
	}
	if err := m.dst.Append(dstName, flags, date, r); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// Events returns a read-only channel of progress events.
func (m *MailboxSyncer) Events() <-chan Event { return m.events }

func (m *MailboxSyncer) emit(ev Event) {
	select {
	case m.events <- ev:
	default:
		// drop if slow consumer
	}
}

func (m *MailboxSyncer) mapName(name string) string {
	if m.opts.Map == nil {
		return name
	}
	if to, ok := m.opts.Map[name]; ok && to != "" {
		return to
	}
	return name
}
