package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const (
	credentialsFile = "credentials.json"
	tokenFileName   = ".gmail-tui-token.json" // #nosec G101
)

// checkGitignore warns if credentials.json is not listed in .gitignore.
func checkGitignore() {
	f, err := os.Open(".gitignore")
	if err != nil {
		slog.Warn("credentials.json is not in .gitignore — risk of committing secrets")
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "credentials.json") {
			return
		}
	}
	slog.Warn("credentials.json is not in .gitignore — risk of committing secrets")
}

func newGmailService() (*gmail.Service, string, error) {
	// Only relevant when the credentials file lives in the working directory.
	if os.Getenv("GMAIL_TUI_CREDENTIALS") == "" {
		checkGitignore()
	}

	ctx := context.Background()

	config, err := loadOAuthConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load OAuth config: %w", err)
	}

	client, grantedScope, err := getAuthenticatedClient(ctx, config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get authenticated client: %w", err)
	}

	srv, err := gmail.NewService(ctx, option.WithHTTPClient(attachmentLimitedClient(client)))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create Gmail service: %w", err)
	}

	return srv, grantedScope, nil
}

func loadOAuthConfig() (*oauth2.Config, error) {
	credPath := os.Getenv("GMAIL_TUI_CREDENTIALS")
	if credPath == "" {
		credPath = credentialsFile
	}

	data, err := os.ReadFile(credPath) // #nosec G703 -- The local user explicitly selects the OAuth credentials path; it is not remote input.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("credentials file not found at %s — see README for setup instructions: %w", credPath, err)
		}
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}

	if !json.Valid(data) {
		return nil, fmt.Errorf("credentials file at %s is not valid JSON — ensure you have downloaded your OAuth client credentials correctly (see README)", credPath)
	}

	scopes := []string{gmail.GmailReadonlyScope, gmail.GmailSendScope, gmail.GmailModifyScope}
	if readOnlyMode() {
		scopes = []string{gmail.GmailReadonlyScope}
	}
	config, err := google.ConfigFromJSON(data, scopes...)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}

	return config, nil
}

// getAuthenticatedClient returns an authenticated HTTP client along with the
// space-separated set of scopes Google actually granted for the token in use.
// Google's consent screen lets a user deselect individual scope checkboxes, so
// the granted set can be a subset of the scopes requested in loadOAuthConfig.
func getAuthenticatedClient(ctx context.Context, config *oauth2.Config) (*http.Client, string, error) {
	tokenPath, err := getTokenPath()
	if err != nil {
		return nil, "", fmt.Errorf("failed to determine token path: %w", err)
	}

	token, scope, err := loadToken(tokenPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("cannot load cached token; repair or remove it before reauthentication: %w", err)
		}
		token, scope, err = performOAuthFlow(ctx, config)
		if err != nil {
			return nil, "", fmt.Errorf("OAuth flow failed: %w", err)
		}
		if err := saveToken(tokenPath, token, scope); err != nil {
			return nil, "", fmt.Errorf("failed to save token: %w", err)
		}
	}

	ts := config.TokenSource(ctx, token)
	refreshedToken, err := ts.Token()
	if err != nil {
		// Reauthorizing with default scopes could expand a narrower old grant.
		// Provider response bodies can also contain tokens; do not surface them.
		return nil, "", errors.New("token refresh failed; remove the cached token and reauthenticate with the intended scope mode")
	}

	if refreshedToken.AccessToken != token.AccessToken ||
		refreshedToken.RefreshToken != token.RefreshToken ||
		!refreshedToken.Expiry.Equal(token.Expiry) {
		if err := saveToken(tokenPath, refreshedToken, scope); err != nil {
			return nil, "", fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	return oauth2.NewClient(ctx, redactedTokenSource{source: ts}), scope, nil
}

func getTokenPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, tokenFileName), nil
}

// storedToken persists the oauth2.Token fields plus the granted scope string,
// since encoding/json on a bare oauth2.Token would otherwise drop it.
type storedToken struct {
	*oauth2.Token
	Scope string `json:"scope,omitempty"`
}

func loadToken(path string) (*oauth2.Token, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, "", errors.New("token must be a private regular file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}

	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, "", errors.New("token file changed while opening")
	}
	stored := storedToken{Token: &oauth2.Token{}}
	decodeErr := json.NewDecoder(f).Decode(&stored)
	f.Close()
	if decodeErr != nil || stored.Token == nil {
		return nil, "", errors.New("failed to decode token")
	}
	return stored.Token, stored.Scope, nil
}

func saveToken(path string, token *oauth2.Token, scope string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".gmail-tui-token-*")
	if err != nil {
		return fmt.Errorf("failed to create temp token file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := json.NewEncoder(f).Encode(storedToken{Token: token, Scope: scope}); err != nil {
		f.Close()
		return fmt.Errorf("failed to encode token: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("failed to sync token: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close token: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil { // #nosec G703 -- CreateTemp supplies the source; the local token destination is atomically replaced without following its symlink.
		return fmt.Errorf("failed to rename token file: %w", err)
	}
	return nil
}

// performOAuthFlow runs the authorization-code flow. It prefers a loopback
// redirect (browser lands on a local callback, no copy-paste) and falls back
// to manual code entry when a local listener cannot be started (e.g. headless
// or sandboxed environments).
func performOAuthFlow(ctx context.Context, config *oauth2.Config) (*oauth2.Token, string, error) {
	token, scope, err := performLoopbackFlow(ctx, config, nil)
	if err == nil {
		return token, scope, nil
	}
	if !errors.Is(err, errLoopbackUnavailable) {
		return nil, "", err
	}
	return performManualFlow(ctx, config)
}

// errLoopbackUnavailable signals that no local listener could be started and
// the manual flow should be used instead.
var errLoopbackUnavailable = errors.New("loopback listener unavailable")

const oauthCallbackTimeout = 5 * time.Minute

// randomState returns a cryptographically random OAuth state parameter.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// grantedScope extracts the scopes Google actually granted from the token
// response; the user may have deselected some via the consent screen's
// granular toggles. Falls back to the requested scopes if omitted.
func grantedScope(token *oauth2.Token, config *oauth2.Config) string {
	scope, ok := token.Extra("scope").(string)
	if !ok || scope == "" {
		scope = strings.Join(config.Scopes, " ")
	}
	return scope
}

type callbackResult struct {
	code string
	err  error
}

// performLoopbackFlow starts a localhost callback server, points the OAuth
// redirect at it, and waits for the browser to deliver the authorization code.
// A nil listener means "listen on an ephemeral localhost port"; tests inject
// their own to know the callback address.
func performLoopbackFlow(ctx context.Context, config *oauth2.Config, ln net.Listener) (*oauth2.Token, string, error) {
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", errLoopbackUnavailable, err)
		}
	}
	defer ln.Close()

	state, err := randomState()
	if err != nil {
		return nil, "", err
	}

	cfg := *config
	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	results := make(chan callbackResult, 1)
	deliver := func(r callbackResult) {
		// Only the first result matters; drop duplicates (retries, stray requests).
		select {
		case results <- r:
		default:
		}
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r) // ignore favicon and other stray requests
				return
			}
			q := r.URL.Query()
			if q.Get("state") != state {
				http.Error(w, "Invalid state parameter.", http.StatusBadRequest)
				deliver(callbackResult{err: errors.New("OAuth state mismatch — possible CSRF, aborting")})
				return
			}
			if q.Get("error") != "" {
				http.Error(w, "Authorization failed.", http.StatusBadRequest)
				deliver(callbackResult{err: errors.New("authorization denied")})
				return
			}
			code := q.Get("code")
			if code == "" {
				http.Error(w, "Missing authorization code.", http.StatusBadRequest)
				deliver(callbackResult{err: fmt.Errorf("callback missing authorization code")})
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><h2>gmail-tui authorized</h2><p>You can close this tab and return to the terminal.</p></body></html>")
			deliver(callbackResult{code: code})
		}),
	}
	go srv.Serve(ln) //nolint:errcheck // returns ErrServerClosed on shutdown
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close()
		}
	}()

	fmt.Printf("\nAuthorization required. Opening your browser; if nothing happens, visit:\n%s\n\nWaiting for authorization...\n", authURL)
	openBrowserFn(authURL)

	select {
	case res := <-results:
		if res.err != nil {
			return nil, "", res.err
		}
		token, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, "", errors.New("failed to exchange authorization code")
		}
		return token, grantedScope(token, &cfg), nil
	case <-time.After(oauthCallbackTimeout):
		return nil, "", fmt.Errorf("timed out waiting for OAuth callback after %s", oauthCallbackTimeout)
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

// performManualFlow prints the auth URL and reads the authorization code from
// stdin. Used when no local listener can be started.
func performManualFlow(ctx context.Context, config *oauth2.Config) (*oauth2.Token, string, error) {
	state, err := randomState()
	if err != nil {
		return nil, "", err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	fmt.Printf("\nAuthorization required. Please visit:\n%s\n\n", authURL)
	fmt.Print("Enter authorization code: ")

	var code string
	if _, err := fmt.Scanln(&code); err != nil {
		return nil, "", fmt.Errorf("failed to read authorization code: %w", err)
	}

	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", errors.New("failed to exchange authorization code")
	}
	return token, grantedScope(token, config), nil
}

// openBrowserFn is a seam so tests can intercept the auth URL instead of
// launching a real browser.
var openBrowserFn = openBrowser

// openBrowser attempts to open url in the default browser; failures are
// non-fatal since the URL is also printed.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// OAuth provider errors may embed response bodies containing credentials. Apply
// redaction to later automatic refreshes too, before API errors reach logs/UI.
type redactedTokenSource struct{ source oauth2.TokenSource }

func (s redactedTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, errors.New("OAuth token refresh failed; reauthenticate with the intended scope mode")
	}
	return token, nil
}
