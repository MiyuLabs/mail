// Package imap — idle.go implements IMAP IDLE support for real-time
// new-mail notifications. Gmail sends an EXISTS update within ~30s
// of a new message arriving. The client re-issues IDLE every 25 minutes
// (well before the RFC-mandated 29-minute server timeout).
package imap

import (
	"log"
	"time"

	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap-idle"
)

const (
	// idleRefreshInterval is how often we re-issue the IDLE command.
	// Must be less than the server's 30-minute idle timeout.
	idleRefreshInterval = 25 * time.Minute
)

// IDLEListener maintains a persistent IMAP IDLE connection and notifies
// via the NewMail channel whenever the server reports new messages.
type IDLEListener struct {
	client  *Client
	NewMail chan struct{}
	stop    chan struct{}
}

// NewIDLEListener creates a new IDLEListener.
func NewIDLEListener(c *Client) *IDLEListener {
	return &IDLEListener{
		client:  c,
		NewMail: make(chan struct{}, 4),
		stop:    make(chan struct{}),
	}
}

// Run starts the IDLE loop. Blocks until Stop() is called.
// Should be run in a goroutine.
func (l *IDLEListener) Run() {
	backoff := 5 * time.Second
	maxBackoff := 2 * time.Minute

	for {
		select {
		case <-l.stop:
			return
		default:
		}

		if err := l.runOnce(); err != nil {
			log.Printf("imap/idle: IDLE error: %v — retrying in %v", err, backoff)
			select {
			case <-l.stop:
				return
			case <-time.After(backoff):
				if err := l.client.Reconnect(); err != nil {
					log.Printf("imap/idle: reconnect failed: %v", err)
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
					continue // Go back to start of loop without resetting backoff
				}
				// Reconnect succeeded, reset backoff
				backoff = 5 * time.Second
			}
		} else {
			// runOnce returned cleanly, reset backoff
			backoff = 5 * time.Second
		}
	}
}

// runOnce executes a single IDLE session and returns when it should be
// re-issued (either due to a server update, timeout, or error).
func (l *IDLEListener) runOnce() error {
	// Ensure INBOX is selected before IDLE.
	if _, err := l.client.SelectInbox(); err != nil {
		return err
	}

	raw := l.client.Raw()
	updates := make(chan client.Update, 8)
	raw.Updates = updates

	idleClient := idle.NewClient(l.client.imap)

	// Start IDLE.
	done := make(chan error, 1)
	go func() {
		done <- idleClient.IdleWithFallback(l.makeIdleStop(), 0)
	}()

	ticker := time.NewTicker(idleRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stop:
			return nil

		case err := <-done:
			raw.Updates = nil
			return err

		case update := <-updates:
			switch update.(type) {
			case *client.MailboxUpdate:
				log.Printf("imap/idle: EXISTS update received — new mail may be available")
				// Signal the sync engine without blocking.
				select {
				case l.NewMail <- struct{}{}:
				default:
				}
			}

		case <-ticker.C:
			// Time to re-issue IDLE. Signal the IDLE goroutine to stop,
			// then loop back to runOnce.
			log.Printf("imap/idle: refreshing IDLE connection")
			raw.Updates = nil
			return nil
		}
	}
}

// makeIdleStop returns a channel that fires when we want to terminate the
// current IDLE command (i.e., on l.stop or after idleRefreshInterval).
func (l *IDLEListener) makeIdleStop() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		select {
		case <-l.stop:
		case <-time.After(idleRefreshInterval):
		}
		close(ch)
	}()
	return ch
}

// Stop signals the IDLE listener to shut down.
func (l *IDLEListener) Stop() {
	close(l.stop)
}
