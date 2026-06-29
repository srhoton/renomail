// Package config loads and saves the renomail TOML configuration and resolves
// the XDG directories the application reads and writes.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Default sync and lookback windows applied when the config omits them.
const (
	defaultSyncInterval = "5m"
	defaultLookback     = "30d"
)

// defaultSlackMaxItems is the digest item-line cap applied when [slack].max_items
// is unset or non-positive.
const defaultSlackMaxItems = 10

// Config is the user-editable application configuration. It mirrors DESIGN.md §8.
// Durations are stored as human-friendly strings and parsed on demand so the
// TOML file stays readable.
type Config struct {
	SyncInterval string         `toml:"sync_interval"` // e.g. "5m"
	Lookback     string         `toml:"lookback"`      // e.g. "30d" (Gmail window)
	Gmail        []GmailAccount `toml:"gmail"`
	OPML         []OPMLSource   `toml:"opml"`
	Feed         []FeedSource   `toml:"feed"` // one-off feeds without OPML

	// TmuxNotifications opts out of the tmux status-line notification fired when
	// new items arrive during a background sync (active only when running inside
	// tmux). It is a pointer so an absent key (default = enabled) is distinct from
	// an explicit `tmux_notifications = false`.
	TmuxNotifications *bool `toml:"tmux_notifications"`

	// MacNotifications opts out of the macOS Notification Center banner posted when
	// unread counts cross a threshold during a background sync (macOS only). Like
	// TmuxNotifications it is a pointer so an absent key (default = enabled) is
	// distinct from an explicit `macos_notifications = false`.
	MacNotifications *bool `toml:"macos_notifications"`

	// Slack configures a Slack incoming-webhook digest posted once per sync sweep
	// that finds new items. It is a pointer so an absent [slack] table leaves Slack
	// disabled. The webhook URL may instead come from the RENOMAIL_SLACK_WEBHOOK
	// environment variable (resolved by the command layer), keeping the secret out
	// of the config file.
	Slack *SlackConfig `toml:"slack"`

	// AppleMail opts the macOS-local, read-only Apple Mail source in. It is a
	// pointer so an absent [apple_mail] table leaves the feature off; when present,
	// `enabled = true` auto-discovers every account Apple Mail has on disk and
	// ingests each account's INBOX. There is no per-account credential — renomail
	// reads Apple Mail's local index read-only (see internal/source/applemail).
	AppleMail *AppleMailConfig `toml:"apple_mail"`
}

// AppleMailConfig gates the local Apple Mail source. A single flag is deliberate:
// when on, all of Apple Mail's accounts are discovered and read (DESIGN.md §9.1).
type AppleMailConfig struct {
	Enabled bool `toml:"enabled"` // false (or an absent [apple_mail] table) ⇒ off
}

// SlackConfig holds the Slack incoming-webhook settings.
type SlackConfig struct {
	WebhookURL string `toml:"webhook_url"` // https://hooks.slack.com/services/...; may be supplied via env instead
	MaxItems   int    `toml:"max_items"`   // max item lines per digest; <= 0 selects the default
}

// GmailAccount identifies a Gmail account to pull. The address doubles as the
// source ID for that account's items.
type GmailAccount struct {
	Account string `toml:"account"` // email address; also the SourceID
}

// OPMLSource points at an OPML file whose feeds should be imported.
type OPMLSource struct {
	Path string `toml:"path"`
}

// FeedSource is a single RSS/Atom feed configured without an OPML file.
type FeedSource struct {
	URL   string `toml:"url"`
	Title string `toml:"title"`
}

// Default returns a Config populated with sensible defaults for a first run.
func Default() Config {
	return Config{SyncInterval: defaultSyncInterval, Lookback: defaultLookback}
}

// withDefaults fills empty duration fields so a partial config file still
// behaves sensibly. It does not touch the slice fields.
func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.SyncInterval) == "" {
		c.SyncInterval = defaultSyncInterval
	}
	if strings.TrimSpace(c.Lookback) == "" {
		c.Lookback = defaultLookback
	}
	return c
}

// Load reads the config file at path. A missing file is not an error: the first
// run returns Default(). Read or parse failures are wrapped with context.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	c = c.withDefaults()

	// Validate the duration fields once, at load time, so a malformed config
	// fails fast at startup rather than deferring the error to a call site.
	if _, err := c.SyncEvery(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if _, err := c.LookbackDuration(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.validateSlack(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// validateSlack rejects an obviously malformed webhook at load time (fail fast). An
// empty webhook is allowed: it may be supplied via RENOMAIL_SLACK_WEBHOOK instead.
func (c Config) validateSlack() error {
	if c.Slack == nil {
		return nil
	}
	url := strings.TrimSpace(c.Slack.WebhookURL)
	if url != "" && !strings.HasPrefix(url, "https://") {
		return errors.New("slack webhook_url must be an https URL")
	}
	return nil
}

// Save writes c to path as TOML, creating the parent directory if needed. The
// file is written 0600 since it may reference account addresses.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	b, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// SyncEvery returns the parsed sync interval, defaulting when unset. It parses on
// each call; a hot-path caller (e.g. a polling loop) should resolve it once and
// cache the returned Duration rather than calling repeatedly.
func (c Config) SyncEvery() (time.Duration, error) {
	return parseDurationDefault(c.SyncInterval, defaultSyncInterval)
}

// LookbackDuration returns the parsed Gmail lookback window, defaulting when
// unset. Like SyncEvery, it parses on each call; cache the result if it is needed
// repeatedly on a hot path.
func (c Config) LookbackDuration() (time.Duration, error) {
	return parseDurationDefault(c.Lookback, defaultLookback)
}

// NotifyEnabled reports whether tmux notifications are on. They default to on;
// only an explicit `tmux_notifications = false` in the config disables them. The
// caller still gates on actually running inside tmux ($TMUX set).
func (c Config) NotifyEnabled() bool {
	return c.TmuxNotifications == nil || *c.TmuxNotifications
}

// MacNotifyEnabled reports whether macOS Notification Center alerts are on. They
// default to on; only an explicit `macos_notifications = false` in the config
// disables them. The caller still gates on actually running on macOS.
func (c Config) MacNotifyEnabled() bool {
	return c.MacNotifications == nil || *c.MacNotifications
}

// SlackMaxItems returns the configured digest item-line cap, falling back to the
// default when [slack] is absent or max_items is unset/non-positive.
func (c Config) SlackMaxItems() int {
	if c.Slack != nil && c.Slack.MaxItems > 0 {
		return c.Slack.MaxItems
	}
	return defaultSlackMaxItems
}

// AppleMailEnabled reports whether the local Apple Mail source is on. It is off
// unless an [apple_mail] table is present with `enabled = true`. (On non-macOS
// builds the provider layer is a stub, so an enabled flag there yields no
// providers and a single advisory warning rather than an error.)
func (c Config) AppleMailEnabled() bool {
	return c.AppleMail != nil && c.AppleMail.Enabled
}

// parseDurationDefault parses s, substituting def when s is empty.
func parseDurationDefault(s, def string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		s = def
	}
	return parseDuration(s)
}

// parseDuration extends time.ParseDuration with a day ("d") suffix, which the
// standard library does not understand (e.g. "30d" -> 720h). Fractional days are
// supported (e.g. "1.5d" -> 36h). Anything without a trailing "d" is delegated
// unchanged.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return d, nil
}
