// Package imap — idle.go owns the IDLE session lifecycle and reconnect loop.
package imap

import (
	"errors"
	"log"
	"sync"
	"time"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"
)

const (
	// Leave headroom before the server's common 30-minute IDLE limit.
	idleRefreshInterval = 25 * time.Minute
	idleInitialBackoff  = 5 * time.Second
	idleMaxBackoff      = 2 * time.Minute
	idleStableSession   = time.Minute
)

var errUnexpectedIdleExit = errors.New("IDLE session ended unexpectedly")

// IDLEListener owns one IDLE lifecycle and reports mailbox changes.
type IDLEListener struct {
	client       *Client
	NewMail      chan struct{}
	FlagChanges  chan struct{}
	stop         chan struct{}
	flagMu       sync.Mutex
	pendingFlags map[uint32]bool

	stopOnce sync.Once
	runOnce  sync.Once

	// Injectable lifecycle operations keep recovery tests deterministic.
	selectMailbox func() error
	idleSession   func(<-chan struct{}, chan<- client.Update) error
	connect       func() error
	connected     func() bool
	reconnect     func() error
	closeClient   func()
	wait          func(<-chan struct{}, time.Duration) bool
	onUpdate      func(client.Update)
	refresh       time.Duration
}

// NewIDLEListener creates an IDLE listener for an already connected client.
func NewIDLEListener(c *Client) *IDLEListener {
	l := &IDLEListener{
		client:       c,
		NewMail:      make(chan struct{}, 4),
		FlagChanges:  make(chan struct{}, 1),
		stop:         make(chan struct{}),
		pendingFlags: make(map[uint32]bool),
		refresh:      idleRefreshInterval,
	}
	l.selectMailbox = func() error {
		_, err := c.SelectInbox()
		return err
	}
	l.connect = c.Connect
	l.connected = c.IsConnected
	l.idleSession = func(stop <-chan struct{}, updates chan<- client.Update) error {
		raw := c.Raw()
		raw.Updates = updates
		defer func() { raw.Updates = nil }()
		return idle.NewClient(raw).IdleWithFallback(stop, 0)
	}
	l.reconnect = c.Reconnect
	l.closeClient = c.Close
	l.wait = waitBackoff
	return l
}

// Run starts the single lifecycle loop and blocks until Stop is called.
func (l *IDLEListener) Run() {
	l.runOnce.Do(func() {
		defer func() {
			if l.closeClient != nil {
				l.closeClient()
			}
		}()
		backoff := idleInitialBackoff
		for {
			if l.isStopped() {
				return
			}
			if l.connected != nil && !l.connected() {
				if err := l.connect(); err != nil {
					log.Printf("imap/idle: initial connect failed: %v", err)
					if !l.wait(l.stop, backoff) {
						return
					}
					backoff = nextBackoff(backoff)
					continue
				}
			}
			duration, err := l.runSession()
			if l.isStopped() {
				return
			}
			if err == nil && duration >= idleStableSession {
				backoff = idleInitialBackoff
				continue
			}
			if err == nil {
				err = errUnexpectedIdleExit
			}
			log.Printf("imap/idle: session ended: %v", err)

			for !l.isStopped() {
				if !l.wait(l.stop, backoff) {
					return
				}
				if err := l.reconnect(); err != nil {
					log.Printf("imap/idle: reconnect failed: %v", err)
					backoff = nextBackoff(backoff)
					continue
				}
				l.notifyMailboxChange()
				backoff = nextBackoff(backoff)
				break
			}
		}
	})
}

func nextBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > idleMaxBackoff {
		return idleMaxBackoff
	}
	return current
}

// runSession starts one IDLE command and always stops and joins it before the
// connection can be reused by a later session.
func (l *IDLEListener) runSession() (time.Duration, error) {
	if err := l.selectMailbox(); err != nil {
		return 0, err
	}
	updates := make(chan client.Update, 16)
	idleStop := make(chan struct{})
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- l.idleSession(idleStop, updates) }()
	ticker := time.NewTimer(l.refresh)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			close(idleStop)
			return time.Since(started), err
		case update := <-updates:
			l.processUpdate(update)
		case <-ticker.C:
			close(idleStop)
			return time.Since(started), <-done
		case <-l.stop:
			close(idleStop)
			return time.Since(started), <-done
		}
	}
}

func (l *IDLEListener) processUpdate(update client.Update) {
	if l.onUpdate != nil {
		l.onUpdate(update)
	}
	switch event := update.(type) {
	case *client.MailboxUpdate:
		log.Printf("imap/idle: mailbox update received")
		l.notifyMailboxChange()
	case *client.MessageUpdate:
		hasFlags := false
		if event.Message != nil {
			_, hasFlags = event.Message.Items[goimap.FetchFlags]
		}
		if event.Message != nil && event.Message.Uid != 0 && hasFlags {
			seen := false
			for _, flag := range event.Message.Flags {
				if flag == goimap.SeenFlag {
					seen = true
					break
				}
			}
			l.flagMu.Lock()
			l.pendingFlags[event.Message.Uid] = seen
			l.flagMu.Unlock()
			select {
			case l.FlagChanges <- struct{}{}:
			default:
			}
		} else {
			// Without a UID the sequence number cannot safely identify a message.
			l.notifyMailboxChange()
		}
	case *client.ExpungeUpdate:
		// SeqNum is transient; synchronization must reconcile using UIDs.
		log.Printf("imap/idle: EXPUNGE received; requesting mailbox reconciliation")
		l.notifyMailboxChange()
	}
}

// DrainFlagChanges returns the latest coalesced \\Seen state for each UID.
func (l *IDLEListener) DrainFlagChanges() map[uint32]bool {
	l.flagMu.Lock()
	defer l.flagMu.Unlock()
	flags := l.pendingFlags
	l.pendingFlags = make(map[uint32]bool)
	return flags
}

func (l *IDLEListener) notifyMailboxChange() {
	select {
	case l.NewMail <- struct{}{}:
	default:
	}
}

func waitBackoff(stop <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

func (l *IDLEListener) isStopped() bool {
	select {
	case <-l.stop:
		return true
	default:
		return false
	}
}

// Stop is safe to call repeatedly. Run joins the active IDLE command before
// closing the underlying client.
func (l *IDLEListener) Stop() {
	l.stopOnce.Do(func() { close(l.stop) })
}
