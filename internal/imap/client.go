// Package imap provides a Gmail IMAP client with OAuth2 (XOAUTH2) authentication,
// UID-based incremental sync, and IDLE push support.
package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"golang.org/x/oauth2"
)

// xoauth2Client is an inline SASL XOAUTH2 client.
// Format per RFC: "user=<email>\x01auth=Bearer <token>\x01\x01"
type xoauth2Client struct {
	username    string
	accessToken string
}

func (c *xoauth2Client) Start() (mech string, ir []byte, err error) {
	mech = "XOAUTH2"
	ir = []byte(fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", c.username, c.accessToken))
	return
}

func (c *xoauth2Client) Next(_ []byte) ([]byte, error) {
	// Gmail never sends a server challenge for XOAUTH2.
	return nil, nil
}

// Client wraps a go-imap client with connection management and XOAUTH2 auth.
type Client struct {
	mu          sync.Mutex
	imap        *client.Client
	tokenSource oauth2.TokenSource
	email       string
	host        string
	port        int
	connected   bool
}

// NewClient creates a new IMAP client. Call Connect() before use.
func NewClient(email, host string, port int, ts oauth2.TokenSource) *Client {
	return &Client{
		email:       email,
		host:        host,
		port:        port,
		tokenSource: ts,
	}
}

// Email returns the underlying Gmail address used for authentication.
func (c *Client) Email() string {
	return c.email
}

// Connect establishes a TLS connection to the IMAP server and authenticates
// using the XOAUTH2 SASL mechanism.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	tlsCfg := &tls.Config{ServerName: c.host}

	ic, err := client.DialTLS(addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("imap: dial failed to %s: %w", addr, err)
	}

	// Get a fresh access token.
	tok, err := c.tokenSource.Token()
	if err != nil {
		_ = ic.Logout()
		return fmt.Errorf("imap: failed to get access token: %w", err)
	}

	// Authenticate with XOAUTH2.
	saslClient := &xoauth2Client{username: c.email, accessToken: tok.AccessToken}
	if err := ic.Authenticate(saslClient); err != nil {
		_ = ic.Logout()
		return fmt.Errorf("imap: XOAUTH2 authentication failed: %w", err)
	}

	c.imap = ic
	c.connected = true
	log.Printf("imap: connected and authenticated as %s", c.email)
	return nil
}

// Reconnect disconnects (if needed) and reconnects. Used after dropped connections.
func (c *Client) Reconnect() error {
	c.mu.Lock()
	if c.connected && c.imap != nil {
		_ = c.imap.Logout()
		c.connected = false
	}
	c.mu.Unlock()
	return c.Connect()
}

// Close gracefully logs out and closes the IMAP connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected && c.imap != nil {
		_ = c.imap.Logout()
		c.connected = false
	}
}

// SelectInbox selects the INBOX mailbox and returns its current state.
func (c *Client) SelectInbox() (*imap.MailboxStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	mbox, err := c.imap.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("imap: SELECT INBOX failed: %w", err)
	}
	return mbox, nil
}

// SearchNewUIDs returns all UIDs in INBOX greater than lastUID.
// Used for incremental sync.
func (c *Client) SearchNewUIDs(lastUID uint32) ([]uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	criteria := imap.NewSearchCriteria()
	if lastUID > 0 {
		uidSet := new(imap.SeqSet)
		uidSet.AddRange(lastUID+1, 0) // 0 = * (all remaining)
		criteria.Uid = uidSet
	}

	uids, err := c.imap.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("imap: UID SEARCH failed: %w", err)
	}
	return uids, nil
}

// FetchMessages retrieves full RFC 5322 messages for the given UIDs.
// Returns them via the messages channel.
func (c *Client) FetchMessages(uids []uint32, messages chan *imap.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(uids) == 0 {
		close(messages)
		return nil
	}

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	// Fetch the full RFC 5322 body + envelope + flags.
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchInternalDate,
		imap.FetchUid,
		"BODY.PEEK[]", // full raw message, no \Seen flag set
	}

	if err := c.imap.UidFetch(seqSet, items, messages); err != nil {
		return fmt.Errorf("imap: UID FETCH failed: %w", err)
	}
	return nil
}

// FetchMessageHeaders fetches only headers (for fast indexing without downloading bodies).
// Used for messages older than the initial page during background sync.
func (c *Client) FetchMessageHeaders(uids []uint32, messages chan *imap.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(uids) == 0 {
		close(messages)
		return nil
	}

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchInternalDate,
		imap.FetchUid,
		"BODY.PEEK[HEADER]", // headers only
	}

	if err := c.imap.UidFetch(seqSet, items, messages); err != nil {
		return fmt.Errorf("imap: UID FETCH (headers) failed: %w", err)
	}
	return nil
}

// SetSeen marks the given UIDs as read (\Seen flag).
func (c *Client) SetSeen(uids []uint32, seen bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}
	if !seen {
		item = imap.FormatFlagsOp(imap.RemoveFlags, true)
	}

	return c.imap.UidStore(seqSet, item, flags, nil)
}

// SetFlagged marks the given UIDs as starred (\Flagged flag).
func (c *Client) SetFlagged(ctx context.Context, uids []uint32, flagged bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(uids) == 0 {
		return nil
	}

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.FlaggedFlag}
	if !flagged {
		item = imap.FormatFlagsOp(imap.RemoveFlags, true)
	}

	return c.imap.UidStore(seqSet, item, flags, nil)
}

// Archive marks the given UIDs with \Deleted in INBOX.
// In default Gmail IMAP settings, this archives the message (removes it from Inbox but keeps it in All Mail).
func (c *Client) Archive(ctx context.Context, uids []uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(uids) == 0 {
		return nil
	}

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}

	return c.imap.UidStore(seqSet, item, flags, nil)
}

// Trash moves the given UIDs to the [Gmail]/Trash mailbox.
func (c *Client) Trash(ctx context.Context, uids []uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(uids) == 0 {
		return nil
	}

	seqSet := new(imap.SeqSet)
	for _, uid := range uids {
		seqSet.AddNum(uid)
	}

	// For Gmail, we need to find the correct Trash mailbox name, but typically it is "[Gmail]/Trash".
	if err := c.imap.UidCopy(seqSet, "[Gmail]/Trash"); err != nil {
		return fmt.Errorf("imap: UID COPY to Trash failed: %w", err)
	}

	// Mark as deleted in the current mailbox so it gets expunged.
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	if err := c.imap.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("imap: UID STORE \\Deleted failed after copy: %w", err)
	}

	return nil
}

// Capability checks whether the server supports the given capability.
func (c *Client) Capability(cap string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	caps, err := c.imap.Capability()
	if err != nil {
		return false, err
	}
	_, ok := caps[cap]
	return ok, nil
}

// Raw returns the underlying go-imap client for IDLE operations.
// Caller must not hold the mutex while using the raw client.
func (c *Client) Raw() *client.Client {
	return c.imap
}

// WithReconnect calls fn, and if it fails with a connection error, reconnects
// and retries once. This provides basic transient error recovery.
func (c *Client) WithReconnect(fn func() error) error {
	if err := fn(); err != nil {
		log.Printf("imap: operation failed, attempting reconnect: %v", err)
		if rerr := c.Reconnect(); rerr != nil {
			return fmt.Errorf("imap: reconnect failed: %w (original: %v)", rerr, err)
		}
		return fn()
	}
	return nil
}

// KeepAliveLoop sends periodic NOOP commands to keep the connection alive.
// Should be run in a goroutine when IDLE is not active.
func (c *Client) KeepAliveLoop(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.connected && c.imap != nil {
				_ = c.imap.Noop()
			}
			c.mu.Unlock()
		}
	}
}
