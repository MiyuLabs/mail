package imap

import (
	"testing"

	"github.com/emersion/go-imap/client"
)

func TestFindExpungedUIDsIgnoresSequenceNumberShifts(t *testing.T) {
	known := []uint32{101, 205, 309, 412}
	// Two EXPUNGE events may be coalesced into one reconciliation. UIDs are the
	// comparison key; message sequence numbers are intentionally absent here.
	present := []uint32{205, 412}
	got := findExpungedUIDs(known, present)
	want := []uint32{101, 309}
	if len(got) != len(want) {
		t.Fatalf("expunged UIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expunged UIDs = %v, want %v", got, want)
		}
	}
}

func TestIDLEMailboxChangeNotificationCoalescesRapidEvents(t *testing.T) {
	listener := &IDLEListener{NewMail: make(chan struct{}, 1)}
	for i := 0; i < 10; i++ {
		listener.notifyMailboxChange()
	}
	if got := len(listener.NewMail); got != 1 {
		t.Fatalf("queued notifications = %d, want 1 coalesced notification", got)
	}
	// The concrete update type carries a sequence number only. The IDLE handler
	// treats it as a signal to reconcile UIDs, never as an array index.
	var update client.Update = &client.ExpungeUpdate{SeqNum: 2}
	if !isExpungeUpdate(update) {
		t.Fatal("EXPUNGE update was not recognized for UID reconciliation")
	}
}
