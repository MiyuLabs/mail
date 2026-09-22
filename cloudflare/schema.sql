-- D1 Schema for mail
-- Apply with: wrangler d1 execute mail-store --file=schema.sql
-- Or: wrangler d1 execute mail-store --remote --file=schema.sql

-- ── Early index (written by Email Worker before IMAP sync) ────────────────────
CREATE TABLE IF NOT EXISTS message_index (
    message_id   TEXT PRIMARY KEY,
    original_to  TEXT NOT NULL,
    from_address TEXT NOT NULL,
    subject      TEXT NOT NULL,
    in_reply_to  TEXT,
    "references" TEXT,
    received_at  TEXT NOT NULL
);

-- ── Virtual mailbox identities ────────────────────────────────────────────────
-- identity_type: 'mailbox' = inbound+outbound, 'send_only' = outbound only (e.g. noreply@)
-- NOTE: send_only identities are NOT routed in Cloudflare Email Routing.
--       They exist here solely for outbound sending via Resend.
CREATE TABLE IF NOT EXISTS identities (
    id            TEXT PRIMARY KEY,
    address       TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    identity_type TEXT NOT NULL DEFAULT 'mailbox',
    is_active     INTEGER DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- ── Conversation threads ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS threads (
    id              TEXT PRIMARY KEY,
    subject         TEXT NOT NULL,
    identity_id     TEXT NOT NULL,
    last_message_at TEXT NOT NULL,
    message_count   INTEGER DEFAULT 0,
    is_read         INTEGER DEFAULT 0,
    is_starred      INTEGER DEFAULT 0,
    is_archived     INTEGER DEFAULT 0,
    is_trashed      INTEGER DEFAULT 0,
    snippet         TEXT,
    participant_names TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    FOREIGN KEY (identity_id) REFERENCES identities(id)
);

-- ── Individual messages ───────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS messages (
    id              TEXT PRIMARY KEY,
    thread_id       TEXT NOT NULL,
    message_id      TEXT NOT NULL UNIQUE,   -- RFC 5322 Message-ID
    in_reply_to     TEXT,
    "references"    TEXT,                   -- space-separated list
    from_address    TEXT NOT NULL,
    from_name       TEXT,
    to_addresses    TEXT NOT NULL,          -- JSON array
    cc_addresses    TEXT,                   -- JSON array
    bcc_addresses   TEXT,                   -- JSON array
    reply_to        TEXT,
    subject         TEXT NOT NULL,
    body_text       TEXT,
    body_html       TEXT,
    snippet         TEXT,
    original_to     TEXT,                   -- X-Original-To header value
    direction       TEXT NOT NULL DEFAULT 'inbound',  -- 'inbound' | 'outbound'
    imap_uid        INTEGER,
    resend_id       TEXT,
    is_read         INTEGER DEFAULT 0,
    is_draft        INTEGER DEFAULT 0,
    has_attachments INTEGER DEFAULT 0,
    received_at     TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    FOREIGN KEY (thread_id) REFERENCES threads(id)
);

-- ── Attachments ────────────────────────────────────────────────────────────────
-- Binary content is NOT stored here — it lives in ~/.mail/cache/ on each client.
CREATE TABLE IF NOT EXISTS attachments (
    id           TEXT PRIMARY KEY,
    message_id   TEXT NOT NULL,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    content_id   TEXT,
    is_inline    INTEGER DEFAULT 0,
    storage_key  TEXT,                      -- local cache path or empty
    created_at   TEXT NOT NULL,
    FOREIGN KEY (message_id) REFERENCES messages(id)
);

-- ── Drafts ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS drafts (
    id            TEXT PRIMARY KEY,
    thread_id     TEXT,
    identity_id   TEXT NOT NULL,
    to_addresses  TEXT,                     -- JSON array
    cc_addresses  TEXT,
    bcc_addresses TEXT,
    subject       TEXT,
    body_text     TEXT,
    body_html     TEXT,
    in_reply_to   TEXT,
    "references"  TEXT,
    is_reply      INTEGER DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    FOREIGN KEY (thread_id) REFERENCES threads(id),
    FOREIGN KEY (identity_id) REFERENCES identities(id)
);

-- ── Participants (contact autocomplete) ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS participants (
    id            TEXT PRIMARY KEY,
    address       TEXT NOT NULL UNIQUE,
    display_name  TEXT,
    last_seen_at  TEXT NOT NULL,
    message_count INTEGER DEFAULT 1
);

-- ── Per-client sync cursors ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS sync_cursors (
    client_id   TEXT PRIMARY KEY,
    imap_uid    INTEGER DEFAULT 0,
    d1_cursor   TEXT,
    updated_at  TEXT NOT NULL
);

-- ── Indexes ───────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_messages_thread      ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_message_id  ON messages(message_id);
CREATE INDEX IF NOT EXISTS idx_messages_in_reply_to ON messages(in_reply_to);
CREATE INDEX IF NOT EXISTS idx_messages_original_to ON messages(original_to);
CREATE INDEX IF NOT EXISTS idx_messages_imap_uid    ON messages(imap_uid);
CREATE INDEX IF NOT EXISTS idx_messages_direction   ON messages(direction);
CREATE INDEX IF NOT EXISTS idx_threads_identity     ON threads(identity_id);
CREATE INDEX IF NOT EXISTS idx_threads_last_message ON threads(last_message_at DESC);
CREATE INDEX IF NOT EXISTS idx_threads_not_archived ON threads(is_archived, is_trashed);
CREATE INDEX IF NOT EXISTS idx_drafts_thread        ON drafts(thread_id);
CREATE INDEX IF NOT EXISTS idx_participants_address ON participants(address);

-- ── Seed identities ───────────────────────────────────────────────────────────
-- Uncomment and update with your @domain.tld addresses, then run this migration.
-- INSERT OR IGNORE INTO identities (id, address, display_name, identity_type, is_active, created_at, updated_at)
-- VALUES
--   (lower(hex(randomblob(16))), 'careers@domain.tld', 'Org. Careers', 'mailbox', 1, datetime('now'), datetime('now')),
--   (lower(hex(randomblob(16))), 'legal@domain.tld',   'Org. Legal',   'mailbox', 1, datetime('now'), datetime('now')),
--   (lower(hex(randomblob(16))), 'hi@domain.tld',      'Org.',         'mailbox', 1, datetime('now'), datetime('now')),
--   (lower(hex(randomblob(16))), 'noreply@domain.tld', 'Org.',         'send_only', 1, datetime('now'), datetime('now'));
