package state

import "testing"

func TestStateMaxUID(t *testing.T) {
	st := &State{MailMax: map[string]uint32{}}
	if got := st.GetMaxUID("INBOX"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
	st.SetMaxUID("INBOX", 10)
	st.SetMaxUID("INBOX", 5)
	st.SetMaxUID("INBOX", 15)
	if got := st.GetMaxUID("INBOX"); got != 15 {
		t.Fatalf("expected 15, got %d", got)
	}
}
