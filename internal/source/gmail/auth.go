package gmail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"

	"github.com/srhoton/renomail/internal/config"
)

// scope is the single OAuth2 scope renomail requests: read-only Gmail access.
// Nothing in renomail ever modifies a mailbox, so a broader scope would be an
// unnecessary grant (DESIGN.md §6.1, §10).
const scope = gmailapi.GmailReadonlyScope

// ErrNotAuthorized signals that an account has no stored OAuth token yet, so the
// user must run `renomail auth <account>` once. It is sentinel so callers (the
// provider registry) can detect it with errors.Is and degrade to a warning
// instead of failing the whole run.
var ErrNotAuthorized = errors.New("gmail: not authorized; run `renomail auth <account>`")

// oauthConfig loads the Google Cloud Desktop-app client from credentialsPath and
// binds it to the read-only Gmail scope. The redirect URL is set per-flow by
// Authorize (the loopback listener address is not known until then).
func oauthConfig(credentialsPath string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", credentialsPath, err)
	}
	cfg, err := google.ConfigFromJSON(b, scope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", credentialsPath, err)
	}
	return cfg, nil
}

// saveToken writes t to path as JSON at 0600 (it carries a refresh token, which
// is a long-lived secret), creating the parent directory at 0700 if needed.
func saveToken(path string, t *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir for %s: %w", path, err)
	}
	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write token %s: %w", path, err)
	}
	return nil
}

// loadToken reads a token previously written by saveToken. A missing file is
// reported as ErrNotAuthorized so the caller can prompt for consent rather than
// treating it as a hard failure.
func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotAuthorized
	}
	if err != nil {
		return nil, fmt.Errorf("read token %s: %w", path, err)
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse token %s: %w", path, err)
	}
	return &t, nil
}

// randomState returns an unguessable OAuth2 state value, used to bind the consent
// redirect to this flow and reject forged callbacks (CSRF). It uses crypto/rand
// per the project's Go security rules.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// callbackResult carries the outcome of the loopback redirect from the HTTP
// handler goroutine back to waitForCode.
type callbackResult struct {
	code string
	err  error
}

// callbackHandler returns the HTTP handler for the loopback redirect. It rejects
// a request whose state does not match (a forged or stale callback), extracts the
// authorization code, shows the user a short success page, and reports the result
// exactly once on done. The single-send is enforced with sync.Once so that
// concurrent hits (a browser refresh or a second /callback request, each served
// on its own goroutine) cannot both send and block on the buffered channel. It is
// split out so the validation logic is unit-testable without binding a real socket
// or opening a browser.
func callbackHandler(wantState string, done chan<- callbackResult) http.HandlerFunc {
	var once sync.Once
	send := func(res callbackResult) { once.Do(func() { done <- res }) }
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if gotState := q.Get("state"); gotState != wantState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			send(callbackResult{err: errors.New("oauth state mismatch")})
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization denied", http.StatusBadRequest)
			send(callbackResult{err: fmt.Errorf("authorization denied: %s", e)})
			return
		}
		code := q.Get("code")
		_, _ = fmt.Fprintln(w, "renomail: authorization complete. You can close this tab.")
		if code == "" {
			send(callbackResult{err: errors.New("no authorization code in callback")})
			return
		}
		send(callbackResult{code: code})
	}
}

// waitForCode serves the loopback redirect on ln until the browser delivers the
// authorization code (or the context is cancelled), then shuts the server down.
func waitForCode(ctx context.Context, ln net.Listener, state string) (string, error) {
	done := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", callbackHandler(state, done))
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-done:
		return res.code, res.err
	}
}

// Authorize runs the interactive consent flow for one account and persists the
// resulting token. It binds an ephemeral loopback listener, prints the consent
// URL (step 08 will auto-open a browser), waits for the redirect, exchanges the
// code, and saves the token at 0600. It is only ever invoked by
// `renomail auth <account>`, never during normal headless startup.
func Authorize(ctx context.Context, paths config.Paths, account string) error {
	cfg, err := oauthConfig(paths.Credentials)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind loopback listener: %w", err)
	}
	defer func() { _ = ln.Close() }()
	cfg.RedirectURL = (&url.URL{Scheme: "http", Host: ln.Addr().String(), Path: "/callback"}).String()

	state, err := randomState()
	if err != nil {
		return err
	}
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Open this URL to authorize %s:\n\n%s\n\n", account, authURL)

	code, err := waitForCode(ctx, ln, state)
	if err != nil {
		return fmt.Errorf("await authorization: %w", err)
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	if err := saveToken(paths.TokenFile(account), tok); err != nil {
		return err
	}
	fmt.Printf("Saved credentials for %s.\n", account)
	return nil
}
