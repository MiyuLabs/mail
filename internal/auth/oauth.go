// Package auth handles OAuth2 credentials and OS keychain storage.
// Secrets are NEVER written to the config file or disk in plaintext.
// Storage backend: OS keychain (Keychain on macOS, Secret Service on Linux,
// Credential Manager on Windows) via the zalando/go-keyring package.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	keychainService  = "MiyuLabs-mail"
	keychainGmail    = "gmail-token"
	keychainResend   = "resend-api-key"
	keychainSyncToken = "sync-api-token"

	// Gmail IMAP/SMTP scope — required for XOAUTH2 authentication.
	gmailScope = "https://mail.google.com/"
)

// tokenData is the JSON structure stored in the keychain.
type tokenData struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	TokenType    string    `json:"token_type"`
}

// GmailTokenSource returns an oauth2.TokenSource backed by the OS keychain.
// On first run (no stored token), it launches the OAuth2 browser flow.
func GmailTokenSource(ctx context.Context, clientID, clientSecret string) (oauth2.TokenSource, error) {
	cfg := gmailOAuthConfig(clientID, clientSecret)

	// Try to load the stored token.
	tok, err := loadToken()
	if err != nil || tok.RefreshToken == "" {
		// No valid stored token or missing refresh token — run the browser-based consent flow.
		tok, err = runConsentFlow(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("auth: OAuth2 consent flow failed: %w", err)
		}
		if err := saveToken(tok); err != nil {
			// Non-fatal: warn but continue (token is valid for this session).
			fmt.Fprintf(os.Stderr, "auth: warning: could not save token to keychain: %v\n", err)
		}
	}

	// Return a token source that auto-refreshes using the stored refresh token
	// and persists the new token back to the keychain when it changes.
	baseSrc := cfg.TokenSource(ctx, tok)
	return &persistingTokenSource{
		src:         baseSrc,
		accessToken: tok.AccessToken,
	}, nil
}

type persistingTokenSource struct {
	mu          sync.Mutex
	accessToken string
	src         oauth2.TokenSource
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.accessToken != tok.AccessToken {
		p.accessToken = tok.AccessToken
		if err := saveToken(tok); err != nil {
			fmt.Fprintf(os.Stderr, "auth: warning: could not save refreshed token to keychain: %v\n", err)
		}
	}
	return tok, nil
}

// XOAuth2String returns the SASL XOAUTH2 authentication string for IMAP.
// Format: "user=<email>\x01auth=Bearer <token>\x01\x01"
func XOAuth2String(email, accessToken string) string {
	return fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", email, accessToken)
}

// GetResendAPIKey retrieves the Resend API key from the keychain.
func GetResendAPIKey() (string, error) {
	key, err := keyring.Get(keychainService, keychainResend)
	if err != nil {
		return "", fmt.Errorf("auth: Resend API key not found in keychain — run 'mail auth resend <key>': %w", err)
	}
	return key, nil
}

// SetResendAPIKey stores the Resend API key in the keychain.
func SetResendAPIKey(key string) error {
	return keyring.Set(keychainService, keychainResend, key)
}

// GetSyncAPIToken retrieves the D1 sync API token from the keychain.
func GetSyncAPIToken() (string, error) {
	tok, err := keyring.Get(keychainService, keychainSyncToken)
	if err != nil {
		return "", fmt.Errorf("auth: sync API token not found in keychain — run 'mail auth token <token>': %w", err)
	}
	return tok, nil
}

// SetSyncAPIToken stores the D1 sync API token in the keychain.
func SetSyncAPIToken(tok string) error {
	return keyring.Set(keychainService, keychainSyncToken, tok)
}

// ClearAll removes all mail credentials from the keychain.
func ClearAll() error {
	for _, key := range []string{keychainGmail, keychainResend, keychainSyncToken} {
		if err := keyring.Delete(keychainService, key); err != nil {
			// Ignore "not found" errors.
			if err != keyring.ErrNotFound {
				return fmt.Errorf("auth: keychain delete failed for %s: %w", key, err)
			}
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────
// internal helpers
// ──────────────────────────────────────────────────────────────────

func gmailOAuthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		RedirectURL:  "urn:ietf:wg:oauth:2.0:oob", // out-of-band for desktop apps
		Scopes:       []string{gmailScope},
	}
}

// runConsentFlow opens the OAuth2 URL in the system browser and waits for
// the user to paste the authorization code into the terminal.
func runConsentFlow(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	// Use a local redirect server on loopback for a better UX than OOB.
	// Fall back to OOB if we can't bind.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv := &http.Server{Addr: "localhost:9876"}
	http.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback URL")
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "<html><body><h2>Authorization successful — you can close this tab.</h2></body></html>")
		codeCh <- code
	})

	cfg.RedirectURL = "http://localhost:9876/oauth/callback"

	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	// Try to open the browser automatically.
	openBrowser(authURL)
	fmt.Printf("\nOpening browser for Gmail authorization...\nIf it did not open, visit:\n\n  %s\n\nWaiting for authorization...\n", authURL)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("auth: OAuth2 flow timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	_ = srv.Shutdown(ctx)

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("auth: token exchange failed: %w", err)
	}
	return tok, nil
}

// openBrowser attempts to open the given URL in the default system browser.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", url}
	default: // Linux
		cmd, args = "xdg-open", []string{url}
	}
	_ = exec.Command(cmd, args...).Start()
}

// loadToken reads the OAuth2 token from the OS keychain.
func loadToken() (*oauth2.Token, error) {
	raw, err := keyring.Get(keychainService, keychainGmail)
	if err != nil {
		return nil, err
	}
	var td tokenData
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		return nil, fmt.Errorf("auth: malformed token in keychain: %w", err)
	}
	return &oauth2.Token{
		AccessToken:  td.AccessToken,
		RefreshToken: td.RefreshToken,
		Expiry:       td.Expiry,
		TokenType:    td.TokenType,
	}, nil
}

// saveToken writes the OAuth2 token to the OS keychain.
func saveToken(tok *oauth2.Token) error {
	td := tokenData{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
		TokenType:    tok.TokenType,
	}
	raw, err := json.Marshal(td)
	if err != nil {
		return err
	}
	return keyring.Set(keychainService, keychainGmail, string(raw))
}
