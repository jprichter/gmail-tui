package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

// Bound the JSON response before the generated Google client decodes base64.
// This includes room for JSON metadata above the maximum encoded attachment.
const maxAttachmentResponse = (maxAttachmentSize+2)/3*4 + 64*1024

type attachmentTransport struct{ base http.RoundTripper }

func attachmentLimitedClient(client *http.Client) *http.Client {
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = attachmentTransport{base: base}
	return &clone
}

func (t attachmentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if strings.Contains(req.URL.Path, "/attachments/") {
		if resp.ContentLength > maxAttachmentResponse {
			resp.Body.Close()
			return nil, errors.New("attachment response too large")
		}
		resp.Body = &attachmentResponseBody{ReadCloser: resp.Body, remaining: maxAttachmentResponse}
	}
	return resp, nil
}

type attachmentResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (b *attachmentResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, errors.New("attachment response too large")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
