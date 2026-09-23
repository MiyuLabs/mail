<div align="center">
    <img src="./assets/icon.png" alt="MiyuMail Logo" width="128" />

# MiyuLabs — Email Infrastructure

Lightweight, self-hosted email infrastructure and a native desktop client for small teams managing multiple custom-domain mailboxes (`careers@`, `legal@`, `hi@`) from a single application. Built around free (pay-as-you-scale) infrastructure, with Cloudflare Email Routing for inbound mail, a hidden Gmail IMAP backend for durable storage, Cloudflare D1 for shared state and synchronization, and Resend for outbound delivery. Supports virtual identities, RFC 5322 threading, shared read/archive/star state, synchronized drafts, attachments, and cross-platform native packaging.
</div>

<img width="1702" height="924" alt="MiyuMail Action" src="https://github.com/user-attachments/assets/68737e7b-4695-485c-884a-180b21e8f93b" />

---

## Stack

| Layer | Tech |
|---|---|
| Language | Go 1.27+ |
| UI Framework | Fyne (Cross-platform native GUI) |
| Inbound Routing | Cloudflare Email Routing + Cloudflare Email Workers |
| Shared State Store | Cloudflare D1 (Serverless SQLite) |
| Email Provider (Outbound) | Resend API |
| Storage Backend (Inbound) | Gmail IMAP (Invisible to senders/recipients) |
| Local Cache | SQLite 3 |

---

## Features

- **True Two-Way Sync:** Mark an email as read, archive it, or star it, and the state synchronizes globally across the entire team via Cloudflare D1 and Gmail IMAP IDLE.
- **Invisible Backend Storage:** Uses a single generic Gmail inbox for reliable, massive, free IMAP storage. The Gmail address is *never* exposed to the outside world; all mail appears to originate natively from your custom domain.
- **Multiple Virtual Identities:** Handle `support@`, `careers@`, and `noreply@` all from a single window.
- **Smart Reply Routing:** When you hit reply, MiyuMail automatically detects which virtual identity received the email and sends the reply from that exact address.
- **Native OS Packaging:** Packaged natively for Windows `.exe`, macOS `.app`, and Linux using Fyne.
- **Low Operating Cost:** Leverages generous free tiers (Cloudflare Email Routing, D1, Resend's 100 emails/day, Gmail's 15GB).

---

## Architecture

<img width="1536" height="1024" alt="miyumail_architecture" src="https://github.com/user-attachments/assets/25bbd97e-3b44-4a96-bb3a-c0968965046c" />

### Core Abstractions

| Abstraction | Implementation |
|---|---|
| `SyncEngine` | Coordinates fetching new UIDs from Gmail, parsing RFC 5322 payloads, and persisting to SQLite. |
| `IDLEListener` | Maintains a dedicated background IMAP connection to instantly detect incoming mail. |
| `ThreadEngine` | Groups messages by RFC 5322 `Message-ID`, `In-Reply-To`, and `References` headers. |
| `IdentityResolver` | Resolves which virtual mailbox an email belongs to using `X-Original-To` or `To/Cc` fallbacks. |
| `Sender` | Outbound delivery via the Resend API, enforcing custom domain headers. |

---

## Local Setup & Team Distribution

### 1. Prerequisites

- Go 1.27+ (for building)
- Node.js 24+ / `wrangler` CLI (for deploying Cloudflare Workers)
- A domain managed on Cloudflare
- A generic Gmail account (for IMAP backend storage)
- A Google Cloud Project (for OAuth2 credentials)
- A Resend Account (for outbound delivery)

### 2. Cloudflare & Provider Setup

- **Cloudflare:** Read [cloudflare/README.md](./cloudflare/README.md) to deploy the Email Worker, the Sync API Worker, and the D1 database. Note down your `sync-api worker url` & `MAIL_SYNC_TOKEN`.
- **Gmail:** Enable the Gmail API in Google Cloud Console, and generate an **OAuth client ID** (Desktop app).
   1. Go to [Google Cloud Console](https://console.cloud.google.com/).
   2. Create a project and enable the **Gmail API** (APIs & Services → Library).
   3. Configure the **OAuth consent screen** → **Data access** → **Add or Remove Scopes** → add scope `https://mail.google.com/`.
   4. Create **OAuth client ID** (type: Desktop app), note the `client_id` and `client_secret`.
- **Resend:** Verify your domain on Resend and generate an API key.
   1. Sign up at [resend.com](https://resend.com).
   2. Add and verify your domain under **Domains**.
   3. Follow the DNS instructions (DKIM, SPF, DMARC records).
   4. Copy your `API Key` from **API Keys**.

### 3. Configure the Application

Copy the configuration template and fill in your details:

```bash
cp configs/mail.example.toml ~/.config/mail/config.toml # $HOME/.config/mail/config.toml
```

Update `~/.config/mail/config.toml` with your domain, Gmail OAuth credentials, Resend domain, and Sync API Worker URL.

### 4. Installation & Distribution (For your team)

**Option A: Pre-built Binaries (Recommended)**
You can download the latest pre-packaged application for your OS directly from the [GitHub Releases](../../releases) page.
- **Windows**: Download `MiyuMail-Windows.zip` (contains `MiyuMail.exe`), extract, and run.
- **macOS**: Download `MiyuMail-macOS.zip`, double-click to extract, and drag `MiyuMail.app` to your `Applications` folder.
- **Linux**: Download `MiyuMail-Linux.zip`, extract it, and run `sudo make install` for a system-wide installation or `make user-install` for a user-local installation.

**Option B: Build Manually**
MiyuMail uses Fyne to package the application into a native, standalone bundle (`.app`, `.exe`, or Linux executable). 

```bash
# Install Fyne CLI
go install fyne.io/fyne/v2/cmd/fyne@latest

# Package the application bundle
make package
```

**To distribute to your team:**
1. Send them the packaged executable (e.g., `MiyuMail.app` or `MiyuMail.exe`), or have them download it from Releases.
2. Send them your configured `config.toml` file.
3. Have them place the file at `~/.config/mail/config.toml` (or their OS equivalent config path: `$HOME/.config/mail/config.toml`) before launching the app.

### 5. First-Time Authentication

When a team member launches the app for the first time, or if you run the setup wizard from the source:

```bash
make setup # Alternatively, can also run ./mail setup
```

The app will securely prompt them to:
- Authenticate with the generic Gmail account via OAuth (browser popup).
- Enter the Resend API key.
- Enter the `MAIL_SYNC_TOKEN` (the secret protecting your Cloudflare D1 Sync API).

These secrets will be then stored securely in the OS Keychain.

---

## CLI Commands & Usage

### Make Targets

| Command | Description |
|---|---|
| `make build` | Compiles a raw Go binary into `bin/` (`mail`) |
| `make package` | Bundles a full GUI app (`MiyuMail.app`/`MiyuMail.exe`) using Fyne with the app icon |
| `make setup` | Runs the interactive wizard to input credentials and authenticate Gmail |
| `make run` | Instantly runs the application via `go run` |
| `make auth-gmail` | Triggers just the Gmail OAuth browser flow |

---

## Free Tier Budget

| Service | Free Tier | Typical Usage (5-person team) |
|---|---|---|
| Cloudflare Email Routing | Unlimited | ~50 inbound emails/day |
| Cloudflare Email Worker | 100K requests/day | ~50 invocations/day |
| Cloudflare D1 | 5M reads, 100K writes/day | ~5K reads, ~500 writes |
| Cloudflare Workers (Sync API) | 100K requests/day | ~2K requests/day |
| Resend | 100 emails/day, 3K/month | ~20 outbound/day |
| Gmail | 15GB storage | Grows slowly over years |

**Total Operating Cost: $0/month.**

---

## License

[GNU AGPL v3](LICENSE).
Copyright (c) 2026 MiyuLabs.
