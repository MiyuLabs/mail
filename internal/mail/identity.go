// Package mail — identity.go resolves which virtual @domain.tld address
// a message was originally addressed to, using a multi-step fallback chain.
package mail

import (
	"strings"
)

// IdentityResolver maps incoming messages to their virtual mailbox identity.
// It uses the X-Original-To header (injected by the Cloudflare Email Worker)
// as the primary mechanism, with multiple fallbacks for robustness.
type IdentityResolver struct {
	// identitiesByAddress maps lowercase address → Identity.
	identitiesByAddress map[string]*Identity
	// defaultIdentityID is used as last resort when nothing matches.
	defaultIdentityID string
}

// NewIdentityResolver creates a resolver from a list of known identities.
func NewIdentityResolver(identities []Identity, defaultAddress string) *IdentityResolver {
	r := &IdentityResolver{
		identitiesByAddress: make(map[string]*Identity, len(identities)),
	}
	for i := range identities {
		id := &identities[i]
		r.identitiesByAddress[strings.ToLower(id.Address)] = id
		if strings.EqualFold(id.Address, defaultAddress) {
			r.defaultIdentityID = id.ID
		}
	}
	return r
}

// Resolve determines the identity for an inbound message.
// Returns (identity, true) if matched, or (defaultIdentity, false) if falling back.
//
// Resolution order:
//  1. X-Original-To header (set by Cloudflare Email Worker, most reliable)
//  2. To: header — any address matching a known @domain.tld identity
//  3. Cc: header — same as above
//  4. Default identity (catch-all)
func (r *IdentityResolver) Resolve(m *Message) (*Identity, bool) {
	// 1. X-Original-To (canonical — set by our Email Worker).
	if id := r.lookup(m.OriginalTo); id != nil {
		return id, true
	}

	// 2. To: header — scan for a known @domain.tld address.
	for _, addr := range m.ToAddresses {
		if id := r.lookup(addr); id != nil {
			return id, true
		}
	}

	// 3. Cc: header.
	for _, addr := range m.CcAddresses {
		if id := r.lookup(addr); id != nil {
			return id, true
		}
	}

	// 4. Default identity (last resort).
	if r.defaultIdentityID != "" {
		for _, id := range r.identitiesByAddress {
			if id.ID == r.defaultIdentityID {
				return id, false
			}
		}
	}

	return nil, false
}

// ResolveByAddress returns the identity for the given email address, or nil.
func (r *IdentityResolver) ResolveByAddress(address string) *Identity {
	return r.lookup(address)
}

// lookup performs a case-insensitive identity lookup by email address.
func (r *IdentityResolver) lookup(address string) *Identity {
	if address == "" {
		return nil
	}
	// Strip display name if present ("Foo Bar <foo@bar.com>" → "foo@bar.com").
	addr := extractAddress(address)
	return r.identitiesByAddress[strings.ToLower(addr)]
}

// extractAddress returns just the email address from a string that may include
// a display name, e.g. "Alice <alice@example.com>" → "alice@example.com".
func extractAddress(s string) string {
	s = strings.TrimSpace(s)
	start := strings.Index(s, "<")
	end := strings.LastIndex(s, ">")
	if start >= 0 && end > start {
		return s[start+1 : end]
	}
	return s
}
