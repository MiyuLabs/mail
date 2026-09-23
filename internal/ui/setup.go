package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/MiyuLabs/mail/internal/auth"
	"github.com/MiyuLabs/mail/internal/config"
)

// ShowSetupWizard displays a GUI wizard for first-time configuration.
// When setup completes successfully, it calls onComplete.
func ShowSetupWizard(app fyne.App, onComplete func()) {
	w := app.NewWindow("MiyuMail Setup Wizard")
	w.Resize(fyne.NewSize(500, 450))
	w.CenterOnScreen()

	// 1. Ensure config file exists
	cfgPath, _ := config.DefaultConfigPath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err == nil {
			_ = os.WriteFile(cfgPath, []byte(config.DefaultConfigToml), 0o600)
		}
	}

	// Load existing config (if any) to prefill what we can
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		// Just a fallback if it fails parsing
		cfg = &config.Config{}
	}

	// 2. Create the setup form
	title := widget.NewLabelWithStyle("Welcome to MiyuMail", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	desc := widget.NewLabel("Please provide your API keys to finalize setup. These are stored securely in your OS keychain.")
	desc.Wrapping = fyne.TextWrapWord

	resendEntry := widget.NewPasswordEntry()
	resendEntry.SetPlaceHolder("re_...")
	
	syncEntry := widget.NewPasswordEntry()
	syncEntry.SetPlaceHolder("cloudflare_worker_token")

	// Pre-fill if they already exist in keychain (so they can update them or leave them)
	if r, err := auth.GetResendAPIKey(); err == nil && r != "" {
		resendEntry.SetText(r)
	}
	if s, err := auth.GetSyncAPIToken(); err == nil && s != "" {
		syncEntry.SetText(s)
	}

	form := widget.NewForm(
		widget.NewFormItem("Resend API Key", resendEntry),
		widget.NewFormItem("Sync API Token", syncEntry),
	)

	// 3. Gmail Auth Button
	
	authBtn := widget.NewButton("Authorize Gmail", func() {
		if cfg.Gmail.OAuthClientID == "" {
			dialog.ShowError(fmt.Errorf("Please set oauth_client_id in config.toml first!"), w)
			return
		}
		
		d := dialog.NewInformation("Authorizing...", "Check your web browser to complete Gmail authorization.", w)
		d.Show()
		
		go func() {
			_, err := auth.GmailTokenSource(context.Background(), cfg.Gmail.OAuthClientID, cfg.Gmail.OAuthClientSecret)
			d.Hide()
			if err != nil {
				dialog.ShowError(fmt.Errorf("Gmail Auth Failed: %v", err), w)
			} else {
				dialog.ShowInformation("Success", "Gmail successfully authorized!", w)
			}
		}()
	})
	authBtn.Importance = widget.HighImportance

	// 4. Save & Launch Button
	var launched bool
	saveBtn := widget.NewButton("Save & Launch", func() {
		launched = true
		if strings.TrimSpace(resendEntry.Text) != "" {
			_ = auth.SetResendAPIKey(strings.TrimSpace(resendEntry.Text))
		}
		if strings.TrimSpace(syncEntry.Text) != "" {
			_ = auth.SetSyncAPIToken(strings.TrimSpace(syncEntry.Text))
		}

		// Close setup window and trigger completion callback
		w.Close()
		onComplete()
	})
	saveBtn.Importance = widget.WarningImportance

	content := container.NewVBox(
		title,
		widget.NewSeparator(),
		desc,
		widget.NewLabel(fmt.Sprintf("Config location: %s", cfgPath)),
		widget.NewSeparator(),
		form,
		widget.NewSeparator(),
		widget.NewLabel("Gmail OAuth2 Access:"),
		authBtn,
		widget.NewSeparator(),
		saveBtn,
	)

	w.SetContent(container.NewPadded(content))
	
	w.SetOnClosed(func() {
		if !launched {
			os.Exit(0)
		}
	})

	w.Show()
}
