# 06 — Gmail Provider & OAuth

## Goal

Add the second `Provider`: Gmail over the official API, read-only, with a
per-account OAuth2 loopback flow and lazy body loading. After this step Gmail
messages from one or more accounts are fetched into the store and appear in the
feed interleaved with RSS items.

## Prerequisites

- Step 02 (model, store, `Provider`). Step 04 (so bodies render).
- A Google Cloud **OAuth client (Desktop app)** `credentials.json` placed at
  `<ConfigDir>/credentials.json` by the user (documented in step 08's README).

## Deliverables

```
internal/source/gmail/auth.go        # OAuth loopback flow + token store
internal/source/gmail/gmail.go       # Provider: Fetch (list+metadata), Body (FULL+MIME)
internal/source/gmail/mime.go        # MIME part selection + decoding
internal/source/gmail/mime_test.go
internal/feeds/registry.go           # extended: BuildGmailProviders
cmd/renomail/auth.go                 # `renomail auth <account>` to run consent once
testdata/gmail_multipart.json        # sample message payload for MIME tests
```

```bash
go get golang.org/x/oauth2@latest
go get golang.org/x/oauth2/google@latest
go get google.golang.org/api/gmail/v1@latest
go get google.golang.org/api/option@latest
# optional: go get github.com/zalando/go-keyring@latest
```

## Design detail

### OAuth config & token store (`auth.go`)

Read-only scope only. The loopback flow binds an ephemeral `localhost` listener,
opens the browser, and exchanges the returned code. Refresh tokens persist to
`<ConfigDir>/token-<account>.json` at `0600` (DESIGN.md §6.1, §10).

```go
const scope = gmail.GmailReadonlyScope // "https://www.googleapis.com/auth/gmail.readonly"

// oauthConfig loads the Desktop-app client and sets a loopback redirect.
func oauthConfig(credentialsPath string) (*oauth2.Config, error) {
    b, err := os.ReadFile(credentialsPath)
    if err != nil { return nil, fmt.Errorf("read credentials: %w", err) }
    cfg, err := google.ConfigFromJSON(b, scope)
    if err != nil { return nil, fmt.Errorf("parse credentials: %w", err) }
    return cfg, nil
}

// Authorize runs the interactive consent flow for one account and saves the token.
// Called by `renomail auth <account>`; never during normal headless startup.
func Authorize(ctx context.Context, paths config.Paths, account string) error {
    cfg, err := oauthConfig(paths.Credentials); if err != nil { return err }
    ln, err := net.Listen("tcp", "127.0.0.1:0"); if err != nil { return err }
    defer ln.Close()
    cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

    state := randomState()
    authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
    _ = browser.OpenURL(authURL) // step 08 adds pkg/browser; here print URL as fallback

    code, err := waitForCode(ctx, ln, state) // tiny http handler captures ?code=&state=
    if err != nil { return err }
    tok, err := cfg.Exchange(ctx, code); if err != nil { return fmt.Errorf("exchange: %w", err) }
    return saveToken(paths.TokenFile(account), tok)
}

func saveToken(path string, t *oauth2.Token) error { /* json marshal, WriteFile 0600 */ }
func loadToken(path string) (*oauth2.Token, error)  { /* json unmarshal */ }
```

> The `oauth2.Config.Client(ctx, token)` wrapper auto-refreshes using the stored
> refresh token, so normal runs never need the browser again. If the token file
> is missing, `Fetch` returns a typed `ErrNotAuthorized` so the UI can prompt the
> user to run `renomail auth <account>`.

### Provider (`gmail.go`)

One provider per account; the account email is the `SourceID`/display name.

```go
type Provider struct {
    account string
    svc     *gmail.Service
    lookback time.Duration
}

func New(ctx context.Context, paths config.Paths, account string, lookback time.Duration) (*Provider, error) {
    cfg, err := oauthConfig(paths.Credentials); if err != nil { return nil, err }
    tok, err := loadToken(paths.TokenFile(account))
    if err != nil { return nil, ErrNotAuthorized }
    svc, err := gmail.NewService(ctx, option.WithHTTPClient(cfg.Client(ctx, tok)))
    if err != nil { return nil, fmt.Errorf("gmail service: %w", err) }
    return &Provider{account: account, svc: svc, lookback: lookback}, nil
}

func (p *Provider) ID() string       { return "gmail:" + p.account }
func (p *Provider) Name() string     { return p.account }
func (p *Provider) Kind() model.Kind { return model.KindEmail }
```

#### Fetch — list + per-message metadata (cheap rows)

```go
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]model.Item, error) {
    q := fmt.Sprintf("in:inbox newer_than:%dd", days(p.lookback)) // DESIGN.md §4.1 default 30d
    if !since.IsZero() { q = fmt.Sprintf("in:inbox after:%d", since.Unix()) }

    var items []model.Item
    call := p.svc.Users.Messages.List("me").Q(q)
    err := call.Pages(ctx, func(page *gmail.ListMessagesResponse) error {
        for _, ref := range page.Messages {
            msg, err := p.svc.Users.Messages.Get("me", ref.Id).
                Format("metadata").MetadataHeaders("From", "Subject", "Date").
                Context(ctx).Do()
            if err != nil { return fmt.Errorf("get %s: %w", ref.Id, err) }
            items = append(items, p.toItem(msg))
        }
        return nil
    })
    if err != nil { return nil, fmt.Errorf("list messages: %w", err) }
    return items, nil
}

func (p *Provider) toItem(msg *gmail.Message) model.Item {
    h := headers(msg.Payload.Headers) // map[string]string, case-insensitive
    published := parseDate(h["Date"]) // RFC1123Z-ish; fall back to InternalDate
    return model.Item{
        ID:         model.StableID(p.ID(), msg.Id),
        Kind:       model.KindEmail,
        SourceID:   p.ID(),
        SourceName: p.account,
        Author:     h["From"],
        Title:      h["Subject"],
        Snippet:    html.UnescapeString(msg.Snippet),
        URL:        fmt.Sprintf("https://mail.google.com/mail/u/?authuser=%s#all/%s", p.account, msg.Id),
        Published:  published,
        Fetched:    time.Now(),
        // Body left empty — loaded lazily by Body().
    }
}
```

#### Body — lazy FULL fetch + MIME walk (`mime.go`)

```go
func (p *Provider) Body(ctx context.Context, item *model.Item) error {
    native := strings.TrimPrefix(item.URL, "...") // or store native id; simplest: re-derive from a stored field
    msg, err := p.svc.Users.Messages.Get("me", gmailID(item)).Format("full").Context(ctx).Do()
    if err != nil { return fmt.Errorf("get full %s: %w", item.ID, err) }
    htmlBody, textBody := selectBodies(msg.Payload)
    item.BodyHTML, item.BodyText = htmlBody, textBody
    return nil
}

// selectBodies recursively walks MIME parts, preferring text/html, capturing
// text/plain as the fallback. Bodies are base64url-encoded (RFC 4648).
func selectBodies(part *gmail.MessagePart) (htmlBody, textBody string) {
    if part == nil { return }
    switch {
    case strings.HasPrefix(part.MimeType, "multipart/"):
        for _, sub := range part.Parts {
            h, t := selectBodies(sub)
            if h != "" { htmlBody = h }
            if t != "" { textBody = t }
        }
    case part.MimeType == "text/html" && part.Body != nil:
        htmlBody = decodeB64URL(part.Body.Data)
    case part.MimeType == "text/plain" && part.Body != nil:
        textBody = decodeB64URL(part.Body.Data)
    }
    return
}

func decodeB64URL(s string) string { b, _ := base64.URLEncoding.DecodeString(s); return string(b) }
```

> To recover the Gmail message id from an `Item`, store it. Simplest: keep the
> native id in a dedicated column, OR re-derive by storing it in `URL`/a new
> field. Recommended: add a `native_id` column in the store (small migration) so
> `Body` does not depend on parsing the web URL. Note this back into DESIGN.md §5
> if adopted.

### Registry extension (`feeds/registry.go`)

```go
// BuildGmailProviders constructs one provider per configured account, skipping
// (with a surfaced warning) any account whose token is missing (ErrNotAuthorized).
func BuildGmailProviders(ctx context.Context, cfg config.Config, paths config.Paths) ([]*gmail.Provider, []error)
```

### `auth` subcommand (`cmd/renomail/auth.go`)

```go
// Usage: renomail auth <account@gmail.com>
// Runs the one-time consent flow and writes token-<account>.json.
func runAuth(ctx context.Context, paths config.Paths, account string) error {
    return gmail.Authorize(ctx, paths, account)
}
```

## Implementation flow

1. Add oauth2 + gmail deps.
2. Implement `auth.go` (oauth config, loopback server, token save/load,
   `ErrNotAuthorized`, `Authorize`).
3. Implement `cmd/renomail/auth.go` + dispatch.
4. Implement `mime.go` (`selectBodies`, decode) + tests against the JSON fixture.
5. Implement `gmail.go` (`Provider`, `Fetch`, `toItem`, `Body`, header/date
   helpers).
6. Add the optional `native_id` store column if adopted; extend registry.
7. Tests + `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **MIME tests (fixture, no network):** `selectBodies` on a multipart
  alternative picks `text/html`, captures `text/plain` fallback, and decodes
  base64url correctly; nested multiparts recurse; missing parts yield empty
  strings, not panics.
- **Unit tests:** `toItem` maps headers → fields; `parseDate` handles common
  formats and falls back; `ID()`/`StableID` deterministic.
- **Auth error path:** `New` with no token file returns `ErrNotAuthorized`;
  registry surfaces it as a warning rather than crashing.
- **Manual (live, documented as a human step):** run
  `renomail auth me@gmail.com`, complete browser consent, then `renomail` →
  Gmail messages appear interleaved with RSS; open one → body renders; relaunch →
  no re-consent needed (refresh token works).

## Done checklist

- [ ] OAuth loopback flow + `0600` token persistence; refresh works headlessly.
- [ ] `renomail auth <account>` runs consent once per account.
- [ ] `Fetch` lists inbox (metadata rows); `Body` lazily loads + MIME-decodes.
- [ ] Multi-account; missing token degrades to a warning.
- [ ] MIME/unit tests pass; live manual run verified; `gofmt`/`vet`/`test` green.
