# MiyuMail — Cloudflare Infrastructure

Two Cloudflare Workers + one D1 database powering the backend for the mail client.

## Stack

- [Cloudflare Email Routing](https://developers.cloudflare.com/email-routing/) — routes `*@yourdomain.com` inbound mail
- [Cloudflare Workers](https://developers.cloudflare.com/workers/) — serverless runtime
- [Cloudflare D1](https://developers.cloudflare.com/d1/) — SQLite-backed database at the edge

## Architecture

```
External Sender
  ↓  careers@yourdomain.com
Cloudflare Email Routing
  ↓
mail-email-worker          ← injects X-Original-To header, writes early D1 index
  ↓  FORWARD_ADDRESS (secret)
Gmail Inbox                ← invisible backend, IMAP only

Desktop Client
  ↓  Bearer MAIL_SYNC_TOKEN
mail-sync-api              ← authenticated REST proxy for D1
  ↓
mail-store (D1)            ← threads, messages, drafts, identities, cursors
```

## Workers

| Worker | Directory | Purpose |
|:--|:--|:--|
| `mail-email-worker` | `email-worker/` | Receives inbound mail, injects `X-Original-To`, forwards to Gmail |
| `mail-sync-api` | `sync-api/` | Authenticated REST API over D1 for desktop clients |

## Setup

### 0. Login to Cloudflare

```bash
npx wrangler login
```

Run this after any changes in `email-worker/wrangler.toml` / `sync-api/wrangler.toml` or src to regenerate types.

```bash
npx wrangler types
```

### 1. Create the D1 database

```bash
npx wrangler d1 create mail-store
```

Copy the `database_id` from the output and update it in **both** `wrangler.toml` files:

- `email-worker/wrangler.toml`
- `sync-api/wrangler.toml`

### 2. Apply the schema

```bash
# cd into either of the worker directory
cd email-worker

# Local (for development)
npx wrangler d1 execute mail-store --file=../schema.sql

# Remote (production)
npx wrangler d1 execute mail-store --remote --file=../schema.sql
```

### 3. Seed identities

Edit `schema.sql`, uncomment the `INSERT INTO identities` block at the bottom, fill in your addresses, then re-run the migration:

```sql
INSERT OR IGNORE INTO identities (id, address, display_name, identity_type, is_active, created_at, updated_at)
VALUES
  (lower(hex(randomblob(16))), 'careers@yourdomain.com', 'Org. Careers',  'mailbox',   1, datetime('now'), datetime('now')),
  (lower(hex(randomblob(16))), 'hi@yourdomain.com',      'Org.', 'mailbox',   1, datetime('now'), datetime('now')),
  (lower(hex(randomblob(16))), 'noreply@yourdomain.com', 'Org.', 'send_only', 1, datetime('now'), datetime('now'));
```

> **Note on `send_only` identities:** do **not** add a Cloudflare Email Routing rule for them.
> Cloudflare drops inbound mail to unrouted addresses — which is exactly the behaviour you want for `noreply@`.

### 4. Set secrets

#### `mail-email-worker` — Gmail forwarding address

```bash
cd email-worker
npx wrangler secret put FORWARD_ADDRESS
```

| Secret | Value |
|:--|:--|
| `FORWARD_ADDRESS` | Your internal Gmail address (e.g. `inbox@gmail.com`) |

#### `mail-sync-api` — authentication token

Generate a strong random token:

```bash
openssl rand -hex 32
```

Then set it as a secret:

```bash
cd sync-api
npx wrangler secret put MAIL_SYNC_TOKEN
```

| Secret | Value |
|:--|:--|
| `MAIL_SYNC_TOKEN` | A long random string — keep this private |

Use this same value in your mail client:

```bash
mail auth token <your-token>
# or
export MAIL_SYNC_TOKEN=<your-token>
```

### 5. Configure Cloudflare Email Routing

In the Cloudflare dashboard → **Email → Email Routing** for your domain:

1. Enable Email Routing and add the required MX + SPF DNS records.
2. Add a route for each **mailbox** identity (addresses you want to receive):
   - Action: **Send to Worker**
   - Worker: `mail-email-worker`
3. **Do NOT add routes** for `send_only` identities (e.g. `noreply@`).

### 6. Deploy

```bash
# Email Worker
cd email-worker
npx wrangler deploy

# Sync API
cd ../sync-api
npx wrangler deploy
```

After deploying, copy the `mail-sync-api` worker URL and add it to your mail client config:

```toml
# ~/.config/mail/config.toml
[sync]
api_url = "https://mail-sync-api.<your-subdomain>.workers.dev"
```

## D1 Tables

| Table | Purpose |
|:--|:--|
| `message_index` | Early index written by the Email Worker before IMAP sync |
| `identities` | Virtual mailbox addresses (`mailbox` and `send_only`) |
| `threads` | Conversation thread metadata |
| `messages` | Individual email messages |
| `attachments` | Attachment metadata (binaries stored locally in `~/.mail/cache/`) |
| `drafts` | Saved drafts synced across clients |
| `participants` | Contact list for autocomplete |
| `sync_cursors` | Per-client IMAP UID + D1 cursor for incremental sync |
