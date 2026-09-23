// Command mail is the MiyuLabs email client.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"

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

var version = "dev"

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	// Sub-command routing (version and help only)
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("mail", version)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	application := app.NewWithID("in.miyulabs.mail")
	application.Settings().SetTheme(&ui.MailTheme{})
	bootMainApp(application)
	application.Run()
}

func bootMainApp(application fyne.App) {
	// Attempt to load everything
	cfg, err := config.Load()
	if err != nil {
		launchSetup(application)
		return
	}

	resendKey := os.Getenv("RESEND_API_KEY")
	if resendKey == "" {
		resendKey, err = auth.GetResendAPIKey()
		if err != nil || resendKey == "" {
			launchSetup(application)
			return
		}
	}

	syncToken := os.Getenv("MAIL_SYNC_TOKEN")
	if syncToken == "" {
		syncToken, err = auth.GetSyncAPIToken()
		if err != nil || syncToken == "" {
			launchSetup(application)
			return
		}
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	application.Lifecycle().SetOnStopped(func() {
		cancel()
	})

	tokenSource, err := auth.GmailTokenSource(ctx, cfg.Gmail.OAuthClientID, cfg.Gmail.OAuthClientSecret)
	if err != nil {
		launchSetup(application)
		return
	}

	// IMAP client (Main)
	imapClient := imap.NewClient(cfg.Gmail.Email, cfg.Gmail.IMAPHost, cfg.Gmail.IMAPPort, tokenSource)

	// D1 client
	d1Client := d1.NewClient(cfg.Sync.APIURL, syncToken)

	// Resend sender
	sender := resend.NewSender(resendKey, cfg.Resend.Domain)

	// Fetch identities
	identities, err := d1Client.ListIdentities(ctx)
	if err != nil {
		fatalError(application, "Failed to load identities from D1: %v", err)
		return
	}

	var appStore *store.Store

	threadEngine := mailpkg.NewThreadEngine(
		func(messageID string) (string, bool) {
			id, err := appStore.GetThreadIDByMessageID(ctx, messageID)
			return id, err == nil && id != ""
		},
		func(threadID string) (*mailpkg.Thread, bool) {
			t, err := appStore.GetThread(ctx, threadID)
			return t, err == nil && t != nil
		},
	)
	resolver := mailpkg.NewIdentityResolver(identities, cfg.Identities.Default)

	cacheDir, err := config.CacheDir()
	if err != nil {
		fatalError(application, "Cannot create cache directory: %v", err)
		return
	}

	clientID := loadOrCreateClientID(cacheDir)

	localDBPath := filepath.Join(cacheDir, "local.db")
	localDB, err := localdb.OpenOrCreate(localDBPath)
	if err != nil {
		fatalError(application, "Cannot open local DB: %v", err)
		return
	}

	for _, id := range identities {
		idCopy := id
		if err := localDB.UpsertIdentity(ctx, &idCopy); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to sync identity %s to local DB: %v\n", id.Address, err)
		}
	}

	appStore = store.NewStore(localDB, d1Client, imapClient, cacheDir)
	syncEngine := imap.NewSyncEngine(imapClient, appStore, threadEngine, resolver, cacheDir, clientID, cfg.Sync.FilterUnrouted, cfg.Resend.Domain)

	// Build and run UI
	mainWindow := ui.NewApp(application, appStore, syncEngine, sender)

	go func() {
		// Connect main IMAP client
		if err := imapClient.Connect(); err != nil {
			log.Printf("IMAP connection failed: %v", err)
			return
		}

		if err := syncEngine.InitialSync(ctx); err != nil {
			log.Printf("initial sync failed: %v", err)
		} else {
			mainWindow.RefreshMailList()
		}

		// Connect dedicated IDLE IMAP client
		idleImapClient := imap.NewClient(cfg.Gmail.Email, cfg.Gmail.IMAPHost, cfg.Gmail.IMAPPort, tokenSource)
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

	// The mainWindow (App wrapper) runs its own window show logic, but wait, 
	// ui.NewApp creates the window and calls w.ShowAndRun() inside Run().
	// But application.Run() is already called in main().
	// We should just call mainWindow.Run() ? No, if application.Run() is in main, we just show the window.
	// We need to change ui.App.Run() to just w.Show() instead of application.Run() or w.ShowAndRun(), since main() does it.
	mainWindow.Run()
}

func launchSetup(application fyne.App) {
	ui.ShowSetupWizard(application, func() {
		bootMainApp(application)
	})
}

func fatalError(application fyne.App, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	w := application.NewWindow("MiyuMail - Startup Error")
	dialog.ShowError(fmt.Errorf("%s", msg), w)
	w.Resize(fyne.NewSize(500, 200))
	w.SetOnClosed(func() { os.Exit(1) })
	w.Show()
}

func printUsage() {
	fmt.Print(`MiyuMail — MiyuLabs Email Client

Usage:
  mail                   Launch the desktop app
  mail version           Print version

Environment variables (override keychain — useful for sharing team secrets):
  RESEND_API_KEY         Resend API key
  MAIL_SYNC_TOKEN        D1 sync API token
  MAIL_CONFIG_PATH       Path to config.toml

`)
}

func loadOrCreateClientID(cacheDir string) string {
	path := cacheDir + "/client_id"
	if data, err := os.ReadFile(path); err == nil {
		return string(data)
	}
	id := uuid.New().String()
	_ = os.WriteFile(path, []byte(id), 0o600)
	return id
}
