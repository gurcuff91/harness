package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// reAPIToken extracts the xoxc session token embedded in Slack's
// desktop-app bootstrap page (and, on older workspaces, the web client page):
// the session state is injected as JSON whose api_token holds the xoxc token.
var reAPIToken = regexp.MustCompile(`"api_token"\s*:\s*"(xoxc-[^"]+)"`)

// browserUserAgent is sent on the login page request. Slack rejects
// non-browser agents there with HTTP 403 (API calls are not affected).
const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// NormalizeXoxD returns the d cookie value the way Slack expects it on the
// wire: URL-encoded. DevTools shows the DECODED value (with "/", "+", "="),
// which is what users paste; sent raw, Slack doesn't recognise the session.
// Idempotent — an already-encoded value (contains "%") is left as-is — and
// tolerant of a pasted "d=" prefix and surrounding whitespace/quotes.
func NormalizeXoxD(xoxd string) string {
	v := strings.TrimSpace(xoxd)
	v = strings.TrimPrefix(v, "d=")
	v = strings.Trim(v, `"'`)
	if v == "" || strings.Contains(v, "%") {
		return v
	}
	return url.QueryEscape(v)
}

// DeriveXoxC derives the xoxc session token from the workspace URL and the d
// (xoxd) cookie — pasted exactly as DevTools shows it, already encoded, or
// with a "d=" prefix. It loads the desktop-app bootstrap page
// (<workspace>/ssb/redirect), which embeds the token, falling back to the
// workspace root for older workspaces (whose root still embeds it; current
// ones redirect it to a client shell without the token).
func DeriveXoxC(ctx context.Context, workspaceURL, xoxd string) (string, error) {
	workspaceURL = strings.TrimRight(workspaceURL, "/")
	cookie := NormalizeXoxD(xoxd)
	if cookie == "" {
		return "", fmt.Errorf("slack login: the d (xoxd) cookie is empty")
	}

	var lastErr error
	for _, path := range []string{"/ssb/redirect", "/"} {
		token, err := fetchAPIToken(ctx, workspaceURL+path, cookie)
		if err == nil {
			return token, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// fetchAPIToken GETs pageURL with the session cookie and extracts api_token.
func fetchAPIToken(ctx context.Context, pageURL, cookie string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("slack login: build request: %w", err)
	}
	req.Header.Set("Cookie", "d="+cookie)
	req.Header.Set("User-Agent", browserUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("slack login: GET workspace: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("slack login: workspace returned HTTP %d — check the workspace URL (e.g. https://myco.slack.com) and that the d cookie is from that workspace", resp.StatusCode)
	}
	// Read up to 2MB — the token appears in the bootstrap JSON.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("slack login: read response: %w", err)
	}
	m := reAPIToken.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("slack login: no api_token found in the workspace page — the d cookie is invalid or expired (copy it again from DevTools while logged in)")
	}
	return string(m[1]), nil
}

// VerifyAndSave verifies the xoxc+xoxd pair against auth.test, populates
// user_id and team, and saves the login (secrets to the credentials store,
// workspace identity to the settings store).
func VerifyAndSave(ctx context.Context, workspaceURL, xoxc, xoxd string) (*Credentials, error) {
	xoxd = NormalizeXoxD(xoxd) // persist the wire (encoded) form
	bot := NewBot(workspaceURL, xoxc, xoxd)
	me, err := bot.AuthTest(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack login: auth.test failed: %w", err)
	}

	creds := &Credentials{
		Workspace: strings.TrimRight(workspaceURL, "/"),
		XoxC:      xoxc,
		XoxD:      xoxd,
		UserID:    me.UserID,
		Team:      me.Team,
	}
	if err := SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("slack login: save credentials: %w", err)
	}
	return creds, nil
}
