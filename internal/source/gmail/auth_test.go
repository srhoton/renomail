package gmail

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/srhoton/renomail/internal/config"
)

// credentialsJSON builds a structurally valid Desktop-app client_credentials.json
// with the given token endpoint, enough for google.ConfigFromJSON to parse. The
// secrets are dummy. Tests that need a working Exchange point tokenURI at an
// httptest server; the rest use the default Google endpoint.
func credentialsJSON(tokenURI string) string {
	return `{"installed":{"client_id":"cid.apps.googleusercontent.com",` +
		`"client_secret":"secret","redirect_uris":["http://localhost"],` +
		`"auth_uri":"https://accounts.google.com/o/oauth2/auth",` +
		`"token_uri":"` + tokenURI + `"}}`
}

// minimalCredentials is the shared fixture for tests that never reach token
// exchange (parse-only, or expecting an earlier error).
var minimalCredentials = credentialsJSON("https://oauth2.googleapis.com/token")

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestSaveLoadToken_roundTripAndPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "token-me.json") // sub dir must be created
	want := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer"}

	if err := saveToken(path, want); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token perms = %o, want 600", perm)
	}

	got, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

func TestLoadToken_missing_returnsErrNotAuthorized(t *testing.T) {
	_, err := loadToken(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
}

func TestLoadToken_malformed_returnsError(t *testing.T) {
	path := writeFile(t, t.TempDir(), "bad.json", "{not json")
	_, err := loadToken(path)
	if err == nil || errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want a parse error (not ErrNotAuthorized)", err)
	}
}

func TestOauthConfig(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, dir, "creds.json", minimalCredentials)
	if _, err := oauthConfig(good); err != nil {
		t.Errorf("oauthConfig(valid) error = %v", err)
	}

	bad := writeFile(t, dir, "bad.json", "{not json")
	if _, err := oauthConfig(bad); err == nil {
		t.Error("oauthConfig(bad json) error = nil, want parse error")
	}

	if _, err := oauthConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("oauthConfig(missing) error = nil, want read error")
	}
}

func TestRandomState_nonEmptyAndDistinct(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatalf("randomState: %v", err)
	}
	b, err := randomState()
	if err != nil {
		t.Fatalf("randomState: %v", err)
	}
	if a == "" || a == b {
		t.Errorf("states not unguessable: a=%q b=%q", a, b)
	}
}

func TestCallbackHandler(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantCode string
		wantErr  bool
		wantHTTP int
	}{
		{"valid", "state=s1&code=abc", "abc", false, http.StatusOK},
		{"state mismatch", "state=other&code=abc", "", true, http.StatusBadRequest},
		{"denied", "state=s1&error=access_denied", "", true, http.StatusBadRequest},
		{"missing code", "state=s1", "", true, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan callbackResult, 1)
			h := callbackHandler("s1", done)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/callback?"+tt.query, nil)
			h(rec, req)

			if rec.Code != tt.wantHTTP {
				t.Errorf("http status = %d, want %d", rec.Code, tt.wantHTTP)
			}
			select {
			case res := <-done:
				if tt.wantErr && res.err == nil {
					t.Error("want error, got none")
				}
				if !tt.wantErr && res.err != nil {
					t.Errorf("unexpected error: %v", res.err)
				}
				if res.code != tt.wantCode {
					t.Errorf("code = %q, want %q", res.code, tt.wantCode)
				}
			default:
				t.Error("handler did not report a result")
			}
		})
	}
}

func TestCallbackHandler_concurrentHits_singleResultNoBlock(t *testing.T) {
	done := make(chan callbackResult, 1)
	h := callbackHandler("s1", done)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/callback?state=s1&code=abc", nil)
			h(rec, req)
		}()
	}
	wg.Wait() // must not deadlock: only the first send fires, the rest are suppressed

	res := <-done
	if res.code != "abc" || res.err != nil {
		t.Fatalf("result = %+v, want code abc / no error", res)
	}
	select {
	case extra := <-done:
		t.Fatalf("got a second result: %+v, want exactly one", extra)
	default:
	}
}

func TestWaitForCode_deliversCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		code, err := waitForCode(context.Background(), ln, "s1")
		codeCh <- code
		errCh <- err
	}()

	target := (&url.URL{Scheme: "http", Host: ln.Addr().String(), Path: "/callback",
		RawQuery: "state=s1&code=xyz"}).String()
	resp, err := http.Get(target) //nolint:noctx // test request to a loopback listener
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	_ = resp.Body.Close()

	if got := <-codeCh; got != "xyz" {
		t.Errorf("code = %q, want xyz", got)
	}
	if err := <-errCh; err != nil {
		t.Errorf("waitForCode error = %v", err)
	}
}

func TestWaitForCode_contextCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: waitForCode must return promptly

	if _, err := waitForCode(ctx, ln, "s1"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestAuthorize_endToEnd drives the full consent flow without a real browser or
// Google: a fake token endpoint stands in for Google's, stdout is captured to
// recover the printed consent URL (which carries the loopback redirect and
// state), and a synthetic callback delivers an authorization code. It asserts the
// exchanged token is persisted at 0600.
func TestAuthorize_endToEnd(t *testing.T) {
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokSrv.Close()

	dir := t.TempDir()
	credPath := writeFile(t, dir, "credentials.json", credentialsJSON(tokSrv.URL))
	paths := config.Paths{ConfigDir: dir, Credentials: credPath}

	// Capture stdout so the printed consent URL can be parsed.
	orig := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wPipe

	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(rPipe)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "https://") {
				select {
				case urlCh <- line:
				default:
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- Authorize(context.Background(), paths, "me@example.com") }()

	authURL := <-urlCh
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	redirect, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	redirect.RawQuery = url.Values{
		"state": {u.Query().Get("state")},
		"code":  {"authcode"},
	}.Encode()
	resp, err := http.Get(redirect.String()) //nolint:noctx // loopback callback
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	_ = resp.Body.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	os.Stdout = orig
	_ = wPipe.Close()

	info, err := os.Stat(paths.TokenFile("me@example.com"))
	if err != nil {
		t.Fatalf("token not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token perms = %o, want 600", perm)
	}
}

// TestAuthorize_badCredentials covers the early error path before any listener is
// bound.
func TestAuthorize_badCredentials(t *testing.T) {
	paths := config.Paths{Credentials: filepath.Join(t.TempDir(), "missing.json")}
	if err := Authorize(context.Background(), paths, "me@example.com"); err == nil {
		t.Error("Authorize with missing credentials = nil, want error")
	}
}
