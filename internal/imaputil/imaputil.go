package imaputil

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// DialAndLogin connects and logs into an IMAP server.
func DialAndLogin(ctx context.Context, host string, port int, user, pass string, startTLS bool, tlsConfig *tls.Config) (*client.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	var c *client.Client
	var err error
	if startTLS {
		// Plain connection, then upgrade with STARTTLS
		c, err = client.Dial(addr)
		if err != nil {
			return nil, err
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			_ = c.Logout()
			return nil, err
		}
	} else {
		c, err = client.DialTLS(addr, tlsConfig)
		if err != nil {
			return nil, err
		}
	}
	// Enable raw IMAP wire debug if requested via environment variable
	if os.Getenv("GOMAP_IMAP_DEBUG") == "1" {
		c.SetDebug(os.Stderr)
	}
	// Login
	if err := c.Login(user, pass); err != nil {
		_ = c.Logout()
		return nil, err
	}
	return c, nil
}

// ListMailboxes returns all mailbox names.
func ListMailboxes(ctx context.Context, c *client.Client) ([]string, error) {
	mailboxes := []string{}
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	hasInbox := false
	go func() {
		done <- c.List("", "*", ch)
		close(done)
	}()
	for m := range ch {
		if m != nil {
			mailboxes = append(mailboxes, m.Name)
			if strings.EqualFold(m.Name, "INBOX") {
				hasInbox = true
			}
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if !hasInbox {
		mailboxes = append(mailboxes, "INBOX")
	}
	return mailboxes, nil
}

// SelectMailbox selects a mailbox in read-only or read-write mode.
func SelectMailbox(c *client.Client, name string, readOnly bool) (*imap.MailboxStatus, error) {
	return c.Select(name, readOnly)
}

// SearchUIDsSince returns UIDs since a time and after a minimal UID.
func SearchUIDsSince(c *client.Client, since time.Time, minUID uint32) ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	if !since.IsZero() {
		criteria.Since = since
	}
	if minUID > 0 {
		criteria.Uid = new(imap.SeqSet)
		criteria.Uid.AddRange(uint32(minUID+1), 4294967295)
	}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return nil, err
	}
	return uids, nil
}

// EnsureMailbox tries to select mailbox and creates it if missing.
func EnsureMailbox(c *client.Client, name string) error {
	if _, err := SelectMailbox(c, name, false); err == nil {
		return nil
	}
	if err := c.Create(name); err != nil {
		if _, selErr := SelectMailbox(c, name, false); selErr == nil {
			return nil
		}
		return err
	}
	return nil
}
