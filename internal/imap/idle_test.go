package imap

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func TestRunSessionStopsAndJoinsIDLEBeforeReturning(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	var l *IDLEListener
	l = &IDLEListener{
		NewMail:       make(chan struct{}, 1),
		stop:          make(chan struct{}),
		refresh:       time.Hour,
		selectMailbox: func() error { return nil },
		idleSession: func(stop <-chan struct{}, _ chan<- client.Update) error {
			close(started)
			<-stop
			close(stopped)
			return nil
		},
	}
	done := make(chan struct{})
	go func() {
		l.runSession()
		close(done)
	}()
	<-started
	l.Stop()
	<-done
	select {
	case <-stopped:
	default:
		t.Fatal("runSession returned before the IDLE worker exited")
	}
}

func TestIDLEProcessesRapidAndDuplicateMailboxEvents(t *testing.T) {
	updates := make(chan client.Update, 12)
	for i := 0; i < 12; i++ {
		updates <- &client.MailboxUpdate{}
	}
	started := make(chan struct{})
	drained := make(chan struct{})
	var processed atomic.Int32
	l := &IDLEListener{
		NewMail:       make(chan struct{}, 1),
		stop:          make(chan struct{}),
		refresh:       time.Hour,
		selectMailbox: func() error { return nil },
		idleSession: func(stop <-chan struct{}, out chan<- client.Update) error {
			for len(updates) > 0 {
				out <- <-updates
			}
			close(started)
			<-stop
			return nil
		},
		onUpdate: func(client.Update) {
			if processed.Add(1) == 12 {
				close(drained)
			}
		},
	}
	done := make(chan struct{})
	go func() {
		l.runSession()
		close(done)
	}()
	<-started
	<-drained
	l.Stop()
	<-done
	if got := processed.Load(); got != 12 {
		t.Fatalf("processed updates = %d, want 12", got)
	}
	if got := len(l.NewMail); got != 1 {
		t.Fatalf("queued notifications = %d, want one coalesced notification", got)
	}
}

func TestIDLECoalescesFlagUpdatesByUID(t *testing.T) {
	l := &IDLEListener{
		NewMail: make(chan struct{}, 1), FlagChanges: make(chan struct{}, 1),
		pendingFlags: make(map[uint32]bool),
	}
	l.processUpdate(&client.MessageUpdate{Message: &goimap.Message{
		Uid: 44, Flags: []string{goimap.SeenFlag}, Items: map[goimap.FetchItem]interface{}{goimap.FetchFlags: nil},
	}})
	l.processUpdate(&client.MessageUpdate{Message: &goimap.Message{
		Uid: 44, Items: map[goimap.FetchItem]interface{}{goimap.FetchFlags: nil},
	}})
	if got := l.DrainFlagChanges(); !reflect.DeepEqual(got, map[uint32]bool{44: false}) {
		t.Fatalf("coalesced flags = %v, want latest UID 44 state false", got)
	}
}

func TestIDLEExpungeRequestsSynchronization(t *testing.T) {
	l := &IDLEListener{NewMail: make(chan struct{}, 1)}
	l.processUpdate(&client.ExpungeUpdate{SeqNum: 3})
	select {
	case <-l.NewMail:
	default:
		t.Fatal("EXPUNGE did not request mailbox synchronization")
	}
}

func TestRunRetriesInitialConnectionBeforeSelectingMailbox(t *testing.T) {
	var attempts, selections, sessions atomic.Int32
	var l *IDLEListener
	l = &IDLEListener{
		NewMail:   make(chan struct{}, 1),
		stop:      make(chan struct{}),
		refresh:   time.Hour,
		connected: func() bool { return attempts.Load() >= 2 },
		connect: func() error {
			if attempts.Add(1) == 1 {
				return errors.New("temporary connect failure")
			}
			return nil
		},
		selectMailbox: func() error { selections.Add(1); return nil },
		idleSession: func(<-chan struct{}, chan<- client.Update) error {
			sessions.Add(1)
			l.Stop()
			return nil
		},
		wait: func(_ <-chan struct{}, delay time.Duration) bool {
			if delay != idleInitialBackoff {
				t.Errorf("initial reconnect delay = %v, want %v", delay, idleInitialBackoff)
			}
			return true
		},
		closeClient: func() {},
	}
	l.Run()
	if attempts.Load() != 2 || selections.Load() != 1 || sessions.Load() != 1 {
		t.Fatalf("connect/select/session counts = %d/%d/%d, want 2/1/1", attempts.Load(), selections.Load(), sessions.Load())
	}
}

func TestRunUsesBoundedBackoffAndReconnectsOncePerSession(t *testing.T) {
	var sessions, reconnects, closes atomic.Int32
	var delays []time.Duration
	var l *IDLEListener
	l = &IDLEListener{
		NewMail:       make(chan struct{}, 1),
		stop:          make(chan struct{}),
		refresh:       time.Hour,
		connected:     func() bool { return true },
		selectMailbox: func() error { return nil },
		idleSession: func(<-chan struct{}, chan<- client.Update) error {
			sessions.Add(1)
			return errors.New("temporary network failure")
		},
		wait: func(_ <-chan struct{}, delay time.Duration) bool {
			delays = append(delays, delay)
			if len(delays) == 3 {
				l.Stop()
				return false
			}
			return true
		},
		reconnect: func() error {
			if reconnects.Add(1) == 1 {
				return errors.New("temporary reconnect failure")
			}
			return nil
		},
		closeClient: func() { closes.Add(1) },
	}
	l.Run()
	if got := sessions.Load(); got != 2 {
		t.Fatalf("IDLE sessions = %d, want 2", got)
	}
	if got := reconnects.Load(); got != 2 {
		t.Fatalf("reconnect attempts = %d, want 2", got)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("connection cleanup calls = %d, want 1", got)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if !reflect.DeepEqual(delays, want) {
		t.Fatalf("backoff delays = %v, want %v", delays, want)
	}
}

func TestStopIsIdempotentAndRunCannotStartDuplicateLoops(t *testing.T) {
	var sessions atomic.Int32
	entered := make(chan struct{})
	l := &IDLEListener{
		NewMail:       make(chan struct{}, 1),
		stop:          make(chan struct{}),
		refresh:       time.Hour,
		connected:     func() bool { return true },
		selectMailbox: func() error { return nil },
		idleSession: func(stop <-chan struct{}, _ chan<- client.Update) error {
			sessions.Add(1)
			close(entered)
			<-stop
			return nil
		},
		closeClient: func() {},
	}
	done := make(chan struct{})
	go func() { l.Run(); close(done) }()
	<-entered
	go l.Run()
	l.Stop()
	l.Stop()
	<-done
	if got := sessions.Load(); got != 1 {
		t.Fatalf("IDLE sessions = %d, want 1", got)
	}
}

func TestNextBackoffCapsAtMaximum(t *testing.T) {
	if got := nextBackoff(idleMaxBackoff); got != idleMaxBackoff {
		t.Fatalf("backoff = %v, want capped value %v", got, idleMaxBackoff)
	}
}
