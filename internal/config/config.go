// Package config loads and validates the mail client configuration.
// Config is read from a TOML file at $XDG_CONFIG_HOME/mail/config.toml
// (fallback: ~/.config/mail/config.toml).
// Secrets (OAuth tokens, API keys) are NEVER stored here — they live in
// the OS keychain. See internal/auth/keychain.go.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

// Config is the top-level application configuration.
type Config struct {
	Gmail      GmailConfig      `toml:"gmail"`
	Resend     ResendConfig     `toml:"resend"`
	Sync       SyncConfig       `toml:"sync"`
	Identities IdentitiesConfig `toml:"identities"`
	UI         UIConfig         `toml:"ui"`
}

// GmailConfig holds IMAP and OAuth2 settings for Gmail.
type GmailConfig struct {
	Email           string `toml:"email"`
	OAuthClientID   string `toml:"oauth_client_id"`
	OAuthClientSecret string `toml:"oauth_client_secret"`
	IMAPHost        string `toml:"imap_host"`
	IMAPPort        int    `toml:"imap_port"`
}

// ResendConfig holds Resend API settings.
type ResendConfig struct {
	Domain string `toml:"domain"`
}

// SyncConfig holds D1 sync API and IMAP sync settings.
type SyncConfig struct {
	APIURL              string `toml:"api_url"`
	PollIntervalSeconds int    `toml:"poll_interval_seconds"`
	InitialSyncDays     int    `toml:"initial_sync_days"`
	PageSize            int    `toml:"page_size"`
	// FilterUnrouted skips messages that don't match a known identity.
	// When true (default), only Cloudflare-routed mail is shown.
	FilterUnrouted bool `toml:"filter_unrouted"`
}

// IdentitiesConfig holds identity-related settings.
type IdentitiesConfig struct {
	Default string `toml:"default"`
}

// UIConfig holds UI preferences.
type UIConfig struct {
	Theme       string `toml:"theme"`
	ShowAvatars bool   `toml:"show_avatars"`
}

// Load reads the config file from the default OS-appropriate path.
// It applies sane defaults for optional fields.
func Load() (*Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, fmt.Errorf("config: cannot determine config path: %w", err)
	}
	return LoadFrom(path)
}

// LoadFrom reads the config file from the given path.
func LoadFrom(path string) (*Config, error) {
	cfg := defaults()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config: file not found at %s — copy configs/mail.example.toml and fill in your values", path)
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("config: parse error in %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: validation failed: %w", err)
	}
	return cfg, nil
}

// defaults returns a Config pre-filled with safe defaults.
func defaults() *Config {
	return &Config{
		Gmail: GmailConfig{
			IMAPHost: "imap.gmail.com",
			IMAPPort: 993,
		},
		Sync: SyncConfig{
			PollIntervalSeconds: 30,
			InitialSyncDays:     90,
			PageSize:            30,
			FilterUnrouted:      true, // only show Cloudflare-routed mail by default
		},
		UI: UIConfig{
			Theme:       "dark",
			ShowAvatars: true,
		},
	}
}

// validate checks that all required fields are present.
func (c *Config) validate() error {
	if c.Gmail.Email == "" {
		return errors.New("gmail.email is required")
	}
	if c.Gmail.OAuthClientID == "" {
		return errors.New("gmail.oauth_client_id is required")
	}
	if c.Gmail.OAuthClientSecret == "" {
		return errors.New("gmail.oauth_client_secret is required")
	}
	if c.Resend.Domain == "" {
		return errors.New("resend.domain is required")
	}
	if c.Sync.APIURL == "" {
		return errors.New("sync.api_url is required")
	}
	return nil
}

// DefaultConfigPath returns the OS-appropriate config file path.
// Order: $MAIL_CONFIG_PATH > $XDG_CONFIG_HOME/mail/config.toml > ~/.config/mail/config.toml
func DefaultConfigPath() (string, error) {
	if env := os.Getenv("MAIL_CONFIG_PATH"); env != "" {
		return env, nil
	}

	var base string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support", "mail")
	default: // linux and others follow XDG
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			xdg = filepath.Join(home, ".config")
		}
		base = filepath.Join(xdg, "mail")
	}

	return filepath.Join(base, "config.toml"), nil
}

// CacheDir returns the path to the local attachment/body cache directory.
func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".mail", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("config: cannot create cache dir %s: %w", dir, err)
	}
	return dir, nil
}

// DefaultConfigToml is the embedded default TOML configuration string.
const DefaultConfigToml = `# MiyuMail — Example Configuration
# Copy this to ~/.config/mail/config.toml and fill in your values.
# Secrets (OAuth Tokens, API Keys) are stored in the OS keychain — not here.

[gmail]
email = "yourusername@gmail.com" # Gmail Address that Cloudflare Email Routing forwards to
oauth_client_id     = "YOUR_CLIENT_ID.apps.googleusercontent.com" # OAUTH2 Client credentials from Google cloud console
oauth_client_secret = "GOCSPX-YOUR_SECRET" # See README.md for how to create these.
imap_host = "imap.gmail.com" # IMAP Settings
imap_port = 993 # Defaults work for Gmail

[resend]
domain = "domain.tld" # Your verified sending domain registered in Resend.

[sync]
api_url = "https://mail-sync.YOUR_SUBDOMAIN.workers.dev" # URL of your deployed Cloudflare Sync API Worker.
poll_interval_seconds = 30 # Fallback polling interval (seconds) when IMAP IDLE is unavailable. Leave default if unsure.
initial_sync_days = 90 # How many days back to sync on first run.
page_size = 30 # Threads loaded per page (on-demand scroll).
filter_unrouted = true # Whether to index only emails routed via Cloudflare Email Routing. If true, emails sent directly to the raw Gmail address are silently skipped.

[identities]
default = "hi@domain.tld" # # Default identity for new compose windows. Must match an address configured in your D1 database.

[ui]
theme = "dark" # use "light" for light-theme.
show_avatars = true # # Show sender avatars (initials-based)
`
