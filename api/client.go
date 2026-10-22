package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/api/gmail/v1"
)

const (
	maxAttachmentSize = 25 * 1024 * 1024
)

// Client wraps the Gmail API with caching and concurrent fetching.
type Client struct {
	srv                *gmail.Service
	cache              *lruCache
	cfg                *Config
	logger             *Logger
	sendScopeGranted   bool
	modifyScopeGranted bool
	readOnly           bool
}

// NewClient authenticates and returns a ready-to-use Client.
func NewClient(cfg *Config, logger *Logger) (*Client, error) {
	srv, grantedScope, err := newGmailService()
	if err != nil {
		return nil, err
	}
	return &Client{
		srv:                srv,
		cache:              newLRUCache(cfg.CacheMaxSize),
		cfg:                cfg,
		logger:             logger,
		sendScopeGranted:   hasScope(grantedScope, gmail.GmailSendScope, gmail.GmailComposeScope, gmail.GmailModifyScope, gmail.MailGoogleComScope),
		modifyScopeGranted: hasScope(grantedScope, gmail.GmailModifyScope, gmail.MailGoogleComScope),
		readOnly:           readOnlyMode(),
	}, nil
}

// ---------- read operations ----------

// FetchInbox returns emails from the primary inbox, fetched concurrently.
func (c *Client) FetchInbox(query string, max int64) ([]Email, error) {
	return c.fetchList(query, "", max)
}

// Search returns emails matching query, fetched concurrently.
func (c *Client) Search(query string, max int64) ([]Email, error) {
	return c.fetchList(query, "", max)
}

// FetchByLabel returns emails with the given label, fetched concurrently.
func (c *Client) FetchByLabel(labelID string, max int64) ([]Email, error) {
	return c.fetchList("", labelID, max)
}

// FetchEmail returns a fully-loaded email (body + attachments).
// Returns from cache if already fully loaded.
func (c *Client) FetchEmail(id string) (*Email, error) {
	if e, ok := c.cache.Load(id); ok {
		if e.FullLoaded {
			return e, nil
		}
	}

	msg, err := c.srv.Users.Messages.Get("me", id).Format("full").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	email := parseMessage(msg, true)
	c.cacheStore(email)
	return email, nil
}

// FetchLabels returns all Gmail labels.
func (c *Client) FetchLabels() ([]Label, error) {
	resp, err := c.srv.Users.Labels.List("me").Do()
	if err != nil {
		return nil, fmt.Errorf("fetchLabels: %w", err)
	}
	if resp == nil {
		return []Label{}, nil
	}
	out := make([]Label, len(resp.Labels))
	for i, l := range resp.Labels {
		out[i] = Label{ID: l.Id, Name: l.Name}
	}
	return out, nil
}

// ---------- write operations ----------

// DeleteEmail moves an email to trash and removes it from cache.
func (c *Client) DeleteEmail(id string) error {
	if !c.HasModifyScope() {
		return errors.New("mailbox modification not permitted")
	}
	_, err := c.srv.Users.Messages.Trash("me", id).Do()
	if err != nil {
		return fmt.Errorf("deleteEmail: %w", err)
	}
	c.cache.Delete(id)
	return nil
}

// ToggleRead flips the UNREAD label. Returns the new isUnread state.
func (c *Client) ToggleRead(id string, currentlyUnread bool) (newUnread bool, err error) {
	if !c.HasModifyScope() {
		return currentlyUnread, errors.New("mailbox modification not permitted")
	}
	mod := &gmail.ModifyMessageRequest{}
	if currentlyUnread {
		mod.RemoveLabelIds = []string{"UNREAD"}
	} else {
		mod.AddLabelIds = []string{"UNREAD"}
	}

	_, err = c.srv.Users.Messages.Modify("me", id, mod).Do()
	if err != nil {
		return currentlyUnread, fmt.Errorf("toggleRead: %w", err)
	}

	newUnread = !currentlyUnread
	c.cache.UpdateUnread(id, newUnread)
	return newUnread, nil
}

// ArchiveEmail removes the INBOX label so the message leaves the inbox.
func (c *Client) ArchiveEmail(id string) error {
	if !c.HasModifyScope() {
		return errors.New("mailbox modification not permitted")
	}
	mod := &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"INBOX"}}
	if _, err := c.srv.Users.Messages.Modify("me", id, mod).Do(); err != nil {
		return fmt.Errorf("archiveEmail: %w", err)
	}
	return nil
}

// outgoing describes a message to be built into a raw MIME payload.
type outgoing struct {
	to, cc, bcc, subject, body string
	inReplyTo, references      string
	attachments                []string
}

// sanitizeHeader strips CR/LF so user-supplied values cannot inject headers.
func sanitizeHeader(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, v)
}

// buildRawMessage assembles a multipart/mixed MIME message and returns it
// base64url-encoded, ready for the Gmail API's Raw field.
func buildRawMessage(o outgoing) (string, error) {
	var mimeBody bytes.Buffer
	writer := multipart.NewWriter(&mimeBody)

	textHeader := textproto.MIMEHeader{}
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	textPart, err := writer.CreatePart(textHeader)
	if err != nil {
		return "", fmt.Errorf("failed to create text part: %w", err)
	}
	if _, err := textPart.Write([]byte(o.body)); err != nil {
		return "", fmt.Errorf("failed to write body: %w", err)
	}

	for _, fp := range o.attachments {
		if err := writeAttachment(writer, fp); err != nil {
			return "", err
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close writer: %w", err)
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "To: %s\r\n", sanitizeHeader(o.to))
	if o.cc != "" {
		fmt.Fprintf(&msg, "Cc: %s\r\n", sanitizeHeader(o.cc))
	}
	if o.bcc != "" {
		fmt.Fprintf(&msg, "Bcc: %s\r\n", sanitizeHeader(o.bcc))
	}
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(o.subject)))
	if o.inReplyTo != "" {
		fmt.Fprintf(&msg, "In-Reply-To: %s\r\n", sanitizeHeader(o.inReplyTo))
	}
	if o.references != "" {
		fmt.Fprintf(&msg, "References: %s\r\n", sanitizeHeader(o.references))
	}
	fmt.Fprintf(&msg, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", writer.Boundary())
	msg.Write(mimeBody.Bytes())

	return base64.URLEncoding.EncodeToString(msg.Bytes()), nil
}

// SendEmail constructs a proper MIME message and sends it.
func (c *Client) SendEmail(to, cc, bcc, subject, body string, attachments []string) error {
	if !c.HasSendScope() {
		return errors.New("sending not permitted")
	}
	raw, err := buildRawMessage(outgoing{
		to: to, cc: cc, bcc: bcc, subject: subject, body: body,
		attachments: attachments,
	})
	if err != nil {
		return fmt.Errorf("sendEmail: %w", err)
	}
	if _, err := c.srv.Users.Messages.Send("me", &gmail.Message{Raw: raw}).Do(); err != nil {
		return fmt.Errorf("sendEmail: %w", err)
	}
	return nil
}

// DownloadAttachment saves an attachment to the downloads directory.
// It sanitizes the filename, deduplicates against existing files, and guards
// against path traversal before writing.
func (c *Client) DownloadAttachment(msgID, attachmentID, filename string) (string, error) {
	// 1. Fetch attachment data.
	att, err := c.srv.Users.Messages.Attachments.Get("me", msgID, attachmentID).Do()
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	// Reject oversized metadata and encoded input before allocating decoded data.
	if att.Size < 0 || att.Size > maxAttachmentSize || len(att.Data) > base64.StdEncoding.EncodedLen(maxAttachmentSize) {
		return "", errors.New("attachment too large (25 MiB maximum)")
	}
	data, err := decodeBase64Robust(att.Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode: %w", err)
	}
	if len(data) > maxAttachmentSize {
		return "", errors.New("attachment too large (25 MiB maximum)")
	}
	return saveAttachment(c.cfg.DownloadsDir, filename, data)
}

// FetchInboxPage returns a page of inbox emails along with the next page token.
func (c *Client) FetchInboxPage(query string, max int64, pageToken string) ([]Email, string, error) {
	call := c.srv.Users.Messages.List("me").MaxResults(max).Q(query)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return nil, "", fmt.Errorf("fetchInboxPage: %w", err)
	}
	if resp == nil || len(resp.Messages) == 0 {
		return []Email{}, "", nil
	}
	emails := c.fetchConcurrently(resp.Messages)
	return emails, resp.NextPageToken, nil
}

// GetUserProfile returns the authenticated user's email address.
func (c *Client) GetUserProfile() (string, error) {
	profile, err := c.srv.Users.GetProfile("me").Do()
	if err != nil {
		return "", fmt.Errorf("getUserProfile: %w", err)
	}
	return profile.EmailAddress, nil
}

// SendReply sends a reply to an existing email thread, setting the appropriate
// In-Reply-To and References headers for proper threading.
func (c *Client) SendReply(to, subject, body, inReplyTo, references string, attachments []string) error {
	if !c.HasSendScope() {
		return errors.New("sending not permitted")
	}
	raw, err := buildRawMessage(outgoing{
		to: to, subject: subject, body: body,
		inReplyTo: inReplyTo, references: references,
		attachments: attachments,
	})
	if err != nil {
		return fmt.Errorf("sendReply: %w", err)
	}
	if _, err := c.srv.Users.Messages.Send("me", &gmail.Message{Raw: raw}).Do(); err != nil {
		return fmt.Errorf("sendReply: %w", err)
	}
	return nil
}

// CacheSize returns the number of emails currently held in the cache.
func (c *Client) CacheSize() int {
	return c.cache.Size()
}

// HasSendScope reports effective sending capability, including modify/compose grants.
func (c *Client) HasSendScope() bool {
	return !c.readOnly && c.sendScopeGranted
}

// HasModifyScope reports effective permission to trash, archive or mark messages.
func (c *Client) HasModifyScope() bool { return !c.readOnly && c.modifyScopeGranted }

// ---------- internal ----------

// fetchList gets message IDs then fetches all concurrently.
func (c *Client) fetchList(query, labelID string, max int64) ([]Email, error) {
	call := c.srv.Users.Messages.List("me").MaxResults(max)
	if query != "" {
		call = call.Q(query)
	}
	if labelID != "" {
		call = call.LabelIds(labelID)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("fetchList: %w", err)
	}
	if resp == nil || len(resp.Messages) == 0 {
		return []Email{}, nil
	}

	return c.fetchConcurrently(resp.Messages), nil
}

// fetchConcurrently fetches metadata for all messages using a bounded worker pool.
// Preserves the original ordering from the API response.
func (c *Client) fetchConcurrently(messages []*gmail.Message) []Email {
	results := make([]*Email, len(messages))
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.cfg.MaxConcurrent)

	for i, m := range messages {
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			msg, err := c.srv.Users.Messages.Get("me", id).
				Format("metadata").
				MetadataHeaders("Subject", "From", "Date", "To", "Cc", "Bcc", "Message-ID", "References", "In-Reply-To").
				Do()
			if err != nil {
				c.logger.Error("failed to fetch message", "id", id, "error", err)
				return
			}
			email := parseMessage(msg, false)
			c.cacheStore(email)
			results[idx] = email
		}(i, m.Id)
	}

	wg.Wait()

	// Compact: drop nil (failed) entries while preserving order.
	emails := make([]Email, 0, len(results))
	for _, e := range results {
		if e != nil {
			emails = append(emails, *e)
		}
	}
	return emails
}

// cacheStore inserts or updates an email in the lruCache.
func (c *Client) cacheStore(email *Email) {
	c.cache.Store(email)
}

func writeAttachment(writer *multipart.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open attachment: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}
	if info.Size() > maxAttachmentSize {
		return fmt.Errorf("attachment too large: %s (max 25MB)", filepath.Base(filePath))
	}

	partHeader := textproto.MIMEHeader{}
	mimeType := mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	partHeader.Set("Content-Type", mimeType)
	partHeader.Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filePath)}))
	partHeader.Set("Content-Transfer-Encoding", "base64")

	pw, err := writer.CreatePart(partHeader)
	if err != nil {
		return fmt.Errorf("failed to create attachment part: %w", err)
	}
	encoder := base64.NewEncoder(base64.StdEncoding, pw)
	if _, err := io.Copy(encoder, file); err != nil {
		return fmt.Errorf("failed to write attachment: %w", err)
	}
	return encoder.Close()
}

func sanitizeFilename(name string) string {
	// Strip path separators and traversal sequences.
	name = filepath.Base(name)
	// filepath.Base returns "." for empty string; normalize to empty.
	if name == "." {
		name = "unnamed"
	}
	// Remove any remaining ".." sequences.
	name = strings.ReplaceAll(name, "..", "_")
	// Allow only safe characters.
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) ||
			r == '-' || r == '_' || r == '.' || r == ' ' {
			return r
		}
		return '_'
	}, name)
	if strings.TrimSpace(name) == "" {
		return "unnamed"
	}
	return name
}

// saveAttachment pins the selected directory with os.Root and atomically reserves
// a new basename. O_EXCL rejects existing files and symlinks, including dangling ones.
func saveAttachment(dir, filename string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(absDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := sanitizeFilename(filename)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i <= 9999; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s(%d)%s", base, i, ext)
		}
		f, err := root.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("save failed: %w", err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return "", fmt.Errorf("save failed (partial file retained): %w", err)
		}
		return filepath.Join(absDir, candidate), nil
	}
	return "", errors.New("attachment filename collision limit exhausted")
}

func readOnlyMode() bool { return os.Getenv("GMAIL_TUI_READ_ONLY") == "1" }

func hasScope(granted string, scopes ...string) bool {
	for _, grant := range strings.Fields(granted) {
		for _, scope := range scopes {
			if grant == scope {
				return true
			}
		}
	}
	return false
}

func decodeBase64Robust(s string) ([]byte, error) {
	// 1. Remove whitespace (common source of "illegal base64 data")
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)

	// 2. Try URL-safe decoding (Gmail standard)
	// Try both Raw and Padded
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	if data, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return data, nil
	}

	// 3. Fallback to standard base64
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
