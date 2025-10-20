package state

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// State tracks per-mailbox highest copied UID or a set of completed UIDs.
// Simple implementation: highest UID per mailbox.

type State struct {
	mu      sync.Mutex
	MailMax map[string]uint32 `json:"mail_max_uid"`
	// MboxOffsets stores processed byte offsets for MBOX sources keyed by
	// a composite identifier (e.g., "mbox:/abs/path|dst:MailboxName").
	MboxOffsets map[string]int64 `json:"mbox_offsets"`
}

func Load(path string) (*State, error) {
	st := &State{MailMax: make(map[string]uint32), MboxOffsets: make(map[string]int64)}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (s *State) GetMaxUID(mailbox string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MailMax[mailbox]
}

func (s *State) SetMaxUID(mailbox string, uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.MailMax[mailbox]; !ok || uid > cur {
		s.MailMax[mailbox] = uid
	}
}

// MBOX helpers
func (s *State) GetMboxOffset(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MboxOffsets[key]
}

func (s *State) SetMboxOffset(key string, off int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.MboxOffsets == nil {
		s.MboxOffsets = make(map[string]int64)
	}
	s.MboxOffsets[key] = off
}
