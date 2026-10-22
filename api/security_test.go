package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
)

func TestSecurityTokenReplacement(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "other")
			path := filepath.Join(dir, "token.json")
			if err := os.WriteFile(target, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("old"), 0666); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path+".tmp"); err != nil {
				t.Fatal(err)
			}
			if err := saveToken(path, &oauth2.Token{AccessToken: "new"}, "scope"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "untouched" {
				t.Fatalf("symlink target altered: %q %v", data, err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("unsafe token mode: %v %v", info, err)
			}
			entries, _ := filepath.Glob(filepath.Join(dir, ".gmail-tui-token-*"))
			if len(entries) != 0 {
				t.Fatal("temporary token remains")
			}
		})
	}
	t.Run("rename failure cleanup", func(t *testing.T) {
		dir := t.TempDir()
		destination := filepath.Join(dir, "destination")
		if err := os.Mkdir(destination, 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveToken(destination, &oauth2.Token{}, ""); err == nil {
			t.Fatal("expected rename failure")
		}
		entries, _ := filepath.Glob(filepath.Join(dir, ".gmail-tui-token-*"))
		if len(entries) != 0 {
			t.Fatal("temporary file leaked")
		}
	})
}

func TestSecurityReadOnlyScopes(t *testing.T) {
	t.Setenv("GMAIL_TUI_READ_ONLY", "1")
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"installed":{"client_id":"id","client_secret":"secret","redirect_uris":["http://127.0.0.1"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GMAIL_TUI_CREDENTIALS", path)
	cfg, err := loadOAuthConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != gmail.GmailReadonlyScope {
		t.Fatalf("scopes = %v", cfg.Scopes)
	}
	for _, scope := range []string{gmail.GmailSendScope, gmail.GmailModifyScope, gmail.GmailComposeScope, gmail.MailGoogleComScope} {
		if !hasScope(scope, gmail.GmailSendScope, gmail.GmailModifyScope, gmail.GmailComposeScope, gmail.MailGoogleComScope) {
			t.Fatalf("missed send grant %s", scope)
		}
	}
	if hasScope(gmail.GmailSendScope+".fake", gmail.GmailSendScope) {
		t.Fatal("scope substring accepted")
	}
	c := &Client{sendScopeGranted: true, modifyScopeGranted: true, readOnly: true}
	if c.HasSendScope() || c.HasModifyScope() {
		t.Fatal("read-only policy allowed existing broad token")
	}
	// A nil service makes any accidental API request fail this test.
	if c.DeleteEmail("id") == nil || c.ArchiveEmail("id") == nil {
		t.Fatal("mailbox mutation allowed")
	}
	if _, err := c.ToggleRead("id", true); err == nil {
		t.Fatal("toggle allowed")
	}
	if c.SendEmail("to", "", "", "subject", "body", nil) == nil || c.SendReply("to", "subject", "body", "", "", nil) == nil {
		t.Fatal("send allowed")
	}
	if got := grantedScope((&oauth2.Token{}).WithExtra(map[string]any{"scope": gmail.GmailReadonlyScope}), cfg); got != gmail.GmailReadonlyScope {
		t.Fatal(got)
	}
	// Legacy tokens with no stored scope fail closed, without expanding permissions.
	c = &Client{}
	if c.HasSendScope() || c.HasModifyScope() {
		t.Fatal("unknown grants enabled writes")
	}
}

func TestSecurityAttachmentSymlinksAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "file(1).txt")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	paths := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := saveAttachment(dir, "../file.txt", []byte("download"))
			if err != nil {
				t.Error(err)
				return
			}
			paths <- p
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for p := range paths {
		if seen[p] {
			t.Fatal("concurrent overwrite")
		}
		seen[p] = true
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe file: %v %v", info, err)
		}
	}
	if len(seen) != 16 {
		t.Fatalf("saved %d files", len(seen))
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatal("outside file overwritten")
	}
}

func TestSecurityAttachmentCollisionExhaustion(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= 9999; i++ {
		name := "file.txt"
		if i > 0 {
			name = fmt.Sprintf("file(%d).txt", i)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := saveAttachment(dir, "file.txt", []byte("overwrite")); err == nil {
		t.Fatal("collision exhaustion did not fail closed")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "file.txt"))
	if string(data) != "keep" {
		t.Fatal("original overwritten")
	}
}

func TestSecurityAttachmentSize(t *testing.T) {
	for _, body := range []string{`{"size":26214401,"data":"YQ"}`, `{"size":-1,"data":"YQ"}`, `{"size":1,"data":"` + strings.Repeat("A", 34952537) + `"}`} {
		c := newHTTPTestClient(t, &Config{DownloadsDir: t.TempDir(), CacheMaxSize: 1}, func(*http.Request) (*http.Response, error) { return jsonResponse(body), nil })
		if _, err := c.DownloadAttachment("msg", "att", "file"); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("oversize result: %v", err)
		}
	}
}

func TestSecurityAttachmentResponseBound(t *testing.T) {
	body := &endlessAttachmentBody{}
	client := attachmentLimitedClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
	})})
	resp, err := client.Get("https://gmail.test/messages/msg/attachments/att")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("missing response limit: %v", err)
	}
	if body.read > maxAttachmentResponse+1 || !body.closed {
		t.Fatalf("response not bounded/closed: %+v", body)
	}
}

type endlessAttachmentBody struct {
	read   int64
	closed bool
}

func (b *endlessAttachmentBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	b.read += int64(len(p))
	return len(p), nil
}
func (b *endlessAttachmentBody) Close() error { b.closed = true; return nil }

func TestSecurityTokenLoadRejectsSymlinksAndPublicFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadToken(path); err == nil {
		t.Fatal("accepted public token")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadToken(link); err == nil {
		t.Fatal("accepted symlink token")
	}
}

func TestSecurityGrantedCapabilities(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	cred := filepath.Join(home, "credentials.json")
	if err := os.WriteFile(cred, []byte(`{"installed":{"client_id":"id","client_secret":"secret","redirect_uris":["http://127.0.0.1"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GMAIL_TUI_CREDENTIALS", cred)
	for _, tt := range []struct {
		scope        string
		send, modify bool
	}{
		{gmail.GmailReadonlyScope, false, false}, {gmail.GmailSendScope, true, false},
		{gmail.GmailComposeScope, true, false}, {gmail.GmailModifyScope, true, true},
		{gmail.MailGoogleComScope, true, true}, {gmail.GmailSendScope + ".fake", false, false}, {"", false, false},
	} {
		for _, readonly := range []string{"", "1"} {
			t.Setenv("GMAIL_TUI_READ_ONLY", readonly)
			if err := saveToken(filepath.Join(home, tokenFileName), &oauth2.Token{AccessToken: "offline-test"}, tt.scope); err != nil {
				t.Fatal(err)
			}
			c, err := NewClient(defaultConfig(), NoopLogger())
			if err != nil {
				t.Fatal(err)
			}
			if c.HasSendScope() != (tt.send && readonly == "") || c.HasModifyScope() != (tt.modify && readonly == "") {
				t.Fatalf("wrong capabilities for %q readOnly=%q", tt.scope, readonly)
			}
		}
	}
}

func TestSecurityRefreshFailureDoesNotExpandScopesOrLeak(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	if err := saveToken(filepath.Join(home, tokenFileName), &oauth2.Token{AccessToken: "", RefreshToken: "refresh-secret"}, gmail.GmailReadonlyScope); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := jsonResponse(`{"error":"invalid_grant","error_description":"refresh-secret"}`)
		resp.StatusCode = 400
		return resp, nil
	})})
	cfg := &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: "https://offline.test/token"}, Scopes: []string{gmail.GmailModifyScope}}
	_, _, err := getAuthenticatedClient(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "token refresh failed") || strings.Contains(err.Error(), "refresh-secret") {
		t.Fatalf("unsafe refresh failure: %v", err)
	}
}

func TestSecurityTokenIgnoresPermissiveLegacyTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	legacy := path + ".tmp"
	if err := os.WriteFile(legacy, []byte("unchanged"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := saveToken(path, &oauth2.Token{AccessToken: "private"}, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("legacy temp reused: %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions: %v %v", info, err)
	}
}

func TestSecuritySelectedDownloadDirectorySymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "downloads")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path, err := saveAttachment(link, "file.txt", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(link, "file.txt") {
		t.Fatalf("download path = %q", path)
	}
	data, err := os.ReadFile(filepath.Join(target, "file.txt"))
	if err != nil || string(data) != "data" {
		t.Fatalf("download failed: %q %v", data, err)
	}
}

func TestSecurityAutomaticRefreshRedactsProviderBody(t *testing.T) {
	client := oauth2.NewClient(context.Background(), redactedTokenSource{source: failingTokenSource{}})
	_, err := client.Get("https://not-contacted.test/")
	if err == nil || !strings.Contains(err.Error(), "OAuth token refresh failed") || strings.Contains(err.Error(), "refresh-secret") {
		t.Fatalf("unsafe runtime refresh error: %v", err)
	}
}

type failingTokenSource struct{}

func (failingTokenSource) Token() (*oauth2.Token, error) {
	return nil, &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, Body: []byte("refresh-secret")}
}
