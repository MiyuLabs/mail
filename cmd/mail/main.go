// Command mail is the MiyuLabs email client.
//
// Usage:
//
//	mail                        — launch the desktop client
//	mail setup                  — interactive first-time setup wizard
//	mail auth gmail             — run Gmail OAuth2 consent flow
//	mail auth resend <key>      — store Resend API key in keychain
//	mail auth token <token>     — store D1 sync API token in keychain
//	mail auth clear             — remove all stored credentials
//	mail version                — print version
//
// Environment variable overrides (useful for sharing secrets without re-running auth):
//
//	RESEND_API_KEY   — overrides keychain for Resend API key
//	MAIL_SYNC_TOKEN  — overrides keychain for D1 sync token
//	MAIL_CONFIG_PATH — path to config.toml (default: ~/.config/mail/config.toml)
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
	"golang.org/x/term"

	"github.com/MiyuLabs/mail/internal/auth"
	"github.com/MiyuLabs/mail/internal/config"
	"github.com/MiyuLabs/mail/internal/d1"
	"github.com/MiyuLabs/mail/internal/imap"
	"github.com/MiyuLabs/mail/internal/localdb"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	"github.com/MiyuLabs/mail/internal/resend"
	"github.com/MiyuLabs/mail/internal/store"
	"github.com/MiyuLabs/mail/internal/ui"
	"github.com/google/uuid"
)

func fatalError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)

	a := fyne.CurrentApp()
	if a == nil {
		a = app.NewWithID("in.miyulabs.mail")
	}
	w := a.NewWindow("MiyuMail - Startup Error")
	
	// Create a generic error dialog
	dialog.ShowError(fmt.Errorf("%s", msg), w)
	w.Resize(fyne.NewSize(500, 200))
	w.SetOnClosed(func() {
		os.Exit(1)
	})
	w.ShowAndRun()
	os.Exit(1)
}

var version = "dev"

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	// ── Sub-command routing ───────────────────────────────────────────────────
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "auth":
			runAuthSubcommand(os.Args[2:])
			return
		case "setup":
			runSetupWizard()
			return
		case "version", "--version", "-v":
			fmt.Println("mail", version)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	// ── Load configuration ────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fatalError(
			"Configuration error: %v\n\n"+
				"First time? Run:  mail setup\n"+
				"Or manually copy configs/mail.example.toml to ~/.config/mail/config.toml",
			err)
	}

	// ── Credentials: env vars take priority over keychain ─────────────────────
	// This lets teammates share credentials without running `mail auth` on each machine.
	resendKey := os.Getenv("RESEND_API_KEY")
	if resendKey == "" {
		resendKey, err = auth.GetResendAPIKey()
		if err != nil {
			fatalError(
				"Resend API key not found.\n" +
					"  Option A: mail auth resend <key>\n" +
					"  Option B: export RESEND_API_KEY=<key>")
		}
	}

	syncToken := os.Getenv("MAIL_SYNC_TOKEN")
	if syncToken == "" {
		syncToken, err = auth.GetSyncAPIToken()
		if err != nil {
			fatalError(
				"Sync API token not found.\n" +
					"  Option A: mail auth token <token>\n" +
					"  Option B: export MAIL_SYNC_TOKEN=<token>")
		}
	}

	// ── Gmail OAuth2 token source ─────────────────────────────────────────────
	ctx := context.Background()
	tokenSource, err := auth.GmailTokenSource(ctx, cfg.Gmail.OAuthClientID, cfg.Gmail.OAuthClientSecret)
	if err != nil {
		fatalError("Gmail authentication failed: %v\nRun 'mail auth gmail' to re-authorise.", err)
	}

	// ── IMAP client (Main) ────────────────────────────────────────────────────
	imapClient := imap.NewClient(
		cfg.Gmail.Email,
		cfg.Gmail.IMAPHost,
		cfg.Gmail.IMAPPort,
		tokenSource,
	)
	defer imapClient.Close()

	// ── D1 client ─────────────────────────────────────────────────────────────
	d1Client := d1.NewClient(cfg.Sync.APIURL, syncToken)

	// ── Resend sender ─────────────────────────────────────────────────────────
	sender := resend.NewSender(resendKey, cfg.Resend.Domain)

	// ── Fetch identities ──────────────────────────────────────────────────────
	identities, err := d1Client.ListIdentities(ctx)
	if err != nil {
		fatalError("Failed to load identities from D1: %v", err)
	}

	// ── Threading engine + identity resolver ──────────────────────────────────
	threadEngine := mailpkg.NewThreadEngine()
	resolver := mailpkg.NewIdentityResolver(identities, cfg.Identities.Default)

	// ── Cache directory ───────────────────────────────────────────────────────
	cacheDir, err := config.CacheDir()
	if err != nil {
		fatalError("Cannot create cache directory: %v", err)
	}

	// ── Client ID (stable per installation) ───────────────────────────────────
	clientID := loadOrCreateClientID(cacheDir)

	// ── Local DB ──────────────────────────────────────────────────────────────
	localDBPath := filepath.Join(cacheDir, "local.db")
	localDB, err := localdb.OpenOrCreate(localDBPath)
	if err != nil {
		fatalError("Cannot open local DB: %v", err)
	}
	defer localDB.Close()

	// Sync identities to local DB to satisfy foreign keys
	for _, id := range identities {
		idCopy := id
		if err := localDB.UpsertIdentity(ctx, &idCopy); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to sync identity %s to local DB: %v\n", id.Address, err)
		}
	}

	// ── Store Coordinator ─────────────────────────────────────────────────────
	appStore := store.NewStore(localDB, d1Client, imapClient, cacheDir)

	// ── Sync engine ───────────────────────────────────────────────────────────
	syncEngine := imap.NewSyncEngine(imapClient, appStore, threadEngine, resolver, cacheDir, clientID, cfg.Sync.FilterUnrouted, cfg.Resend.Domain)

	// ── Build and run UI ──────────────────────────────────────────────────────
	application := ui.NewApp(appStore, syncEngine, sender)

	go func() {
		// Connect main IMAP client
		if err := imapClient.Connect(); err != nil {
			log.Printf("IMAP connection failed: %v", err)
			return
		}

		if err := syncEngine.InitialSync(ctx); err != nil {
			log.Printf("initial sync failed: %v", err)
		} else {
			application.RefreshMailList()
		}

		// Connect dedicated IDLE IMAP client
		idleImapClient := imap.NewClient(
			cfg.Gmail.Email,
			cfg.Gmail.IMAPHost,
			cfg.Gmail.IMAPPort,
			tokenSource,
		)
		
		if err := idleImapClient.Connect(); err != nil {
			log.Printf("IDLE IMAP connection failed: %v", err)
			return
		}

		idleListener := imap.NewIDLEListener(idleImapClient)
		go idleListener.Run()
		go func() {
			for range idleListener.NewMail {
				if err := syncEngine.SyncNewMessages(ctx); err != nil {
					log.Printf("post-IDLE sync failed: %v", err)
				}
			}
		}()
	}()

	application.Run()
	os.Exit(0)
}

// ── Setup wizard ──────────────────────────────────────────────────────────────

// runSetupWizard walks a new user through first-time configuration interactively.
// It is safe to re-run — existing keychain entries are updated, existing config
// is kept as-is unless the user provides new values.
func runSetupWizard() {
	r := bufio.NewReader(os.Stdin)
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────┐")
	fmt.Println("  │   mail — first-time setup wizard         │")
	fmt.Println("  └─────────────────────────────────────────┘")
	fmt.Println()

	// ── Config file ───────────────────────────────────────────────────────────
	cfgPath := os.Getenv("MAIL_CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = defaultConfigPathFallback()
	}

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		// Try to copy example config from local source.
		if err := copyExampleConfig(cfgPath); err != nil {
			fmt.Printf("  → Created starter config: %s\n", cfgPath)
		} else {
			fmt.Printf("  ℹ  No config found at %s\n", cfgPath)
			fmt.Printf("     Copy configs/mail.example.toml there and re-run setup.\n\n")
			return
		}
	} else {
		fmt.Printf("  ✓ Config found at %s\n", cfgPath)
	}

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		fmt.Printf("  ✗ Config error: %v\n\n  Edit %s and re-run setup.\n", err, cfgPath)
		return
	}

	// ── Resend API key ────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("  ── Resend API key ──────────────────────────────────────")
	fmt.Println("  (shared by all clients — same key for everyone on the team)")
	fmt.Println("  Leave blank to skip if already set.")
	fmt.Print("  Resend API key: ")
	resendKey := readSecret(r)
	if resendKey != "" {
		if err := auth.SetResendAPIKey(resendKey); err != nil {
			fmt.Println("  ✗ Failed to store:", err)
		} else {
			fmt.Println("  ✓ Resend API key stored in keychain.")
		}
	} else {
		fmt.Println("  → Skipped.")
	}

	// ── Sync API token ────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("  ── Sync API token ──────────────────────────────────────")
	fmt.Println("  (shared by all clients — same token for everyone on the team)")
	fmt.Println("  Leave blank to skip if already set.")
	fmt.Print("  Sync API token: ")
	syncToken := readSecret(r)
	if syncToken != "" {
		if err := auth.SetSyncAPIToken(syncToken); err != nil {
			fmt.Println("  ✗ Failed to store:", err)
		} else {
			fmt.Println("  ✓ Sync API token stored in keychain.")
		}
	} else {
		fmt.Println("  → Skipped.")
	}

	// ── Gmail OAuth ───────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("  ── Gmail OAuth2 ────────────────────────────────────────")
	fmt.Println("  (per-machine — opens your browser to authorize Gmail access)")
	fmt.Print("  Authorize Gmail now? [Y/n]: ")
	answer, _ := r.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || answer == "y" {
		_, err := auth.GmailTokenSource(context.Background(), cfg.Gmail.OAuthClientID, cfg.Gmail.OAuthClientSecret)
		if err != nil {
			fmt.Println("  ✗ Gmail auth failed:", err)
		} else {
			fmt.Println("  ✓ Gmail authorized — token stored in keychain.")
		}
	} else {
		fmt.Println("  → Skipped. Run 'mail auth gmail' later.")
	}

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────┐")
	fmt.Println("  │   Setup complete — run: mail             │")
	fmt.Println("  └─────────────────────────────────────────┘")
	fmt.Println()
}

// ── Auth sub-command ──────────────────────────────────────────────────────────

func runAuthSubcommand(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: mail auth <gmail|resend <key>|token <token>|clear>")
		return
	}

	switch args[0] {
	case "gmail":
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
			os.Exit(1)
		}
		_, err = auth.GmailTokenSource(context.Background(), cfg.Gmail.OAuthClientID, cfg.Gmail.OAuthClientSecret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Gmail auth failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Gmail authorization successful — token stored in keychain.")

	case "resend":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: mail auth resend <api-key>")
			os.Exit(1)
		}
		if err := auth.SetResendAPIKey(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to store Resend API key: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Resend API key stored in keychain.")

	case "token":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: mail auth token <sync-api-token>")
			os.Exit(1)
		}
		if err := auth.SetSyncAPIToken(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to store sync token: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Sync API token stored in keychain.")

	case "clear":
		if err := auth.ClearAll(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to clear credentials: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ All credentials cleared from keychain.")

	default:
		fmt.Fprintf(os.Stderr, "Unknown auth command: %s\n", args[0])
		os.Exit(1)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Print(`MiyuMail — MiyuLabs Email Client

Usage:
  mail                   Launch the desktop app
  mail setup             Interactive first-time setup wizard
  mail auth gmail        Re-authorize Gmail (opens browser)
  mail auth resend <k>   Store Resend API key in OS keychain
  mail auth token <t>    Store sync API token in OS keychain
  mail auth clear        Remove all stored credentials
  mail version           Print version

Environment variables (override keychain — useful for sharing team secrets):
  RESEND_API_KEY         Resend API key
  MAIL_SYNC_TOKEN        D1 sync API token
  MAIL_CONFIG_PATH       Path to config.toml

`)
}

// readSecret reads a line from stdin without echoing (falls back to normal read
// if not a terminal, e.g. in a pipe).
func readSecret(r *bufio.Reader) string {
	if term.IsTerminal(int(syscall.Stdin)) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println() // newline after hidden input
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func defaultConfigPathFallback() string {
	home, _ := os.UserHomeDir()
	return home + "/.config/mail/config.toml"
}

func copyExampleConfig(dest string) error {
	const example = "configs/mail.example.toml"
	data, err := os.ReadFile(example)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest[:strings.LastIndex(dest, "/")], 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

// loadOrCreateClientID returns a stable per-installation UUID.
func loadOrCreateClientID(cacheDir string) string {
	path := cacheDir + "/client_id"
	if data, err := os.ReadFile(path); err == nil {
		return string(data)
	}
	id := uuid.New().String()
	_ = os.WriteFile(path, []byte(id), 0o600)
	return id
}
