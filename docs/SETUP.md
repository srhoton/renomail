# Setup — Gmail OAuth & feeds

renomail reads Gmail through Google's API with a **read-only** scope
(`gmail.readonly`). To do that you create your own Google Cloud OAuth **Desktop**
client, download its `credentials.json`, and run a one-time consent flow per
account. RSS-only users can skip straight to [Feeds (OPML)](#feeds-opml).

> renomail never modifies your mailbox. It requests only `gmail.readonly`, and
> read/unread state is tracked locally — it is never written back to Gmail.

## 1. Create a Google Cloud project

1. Open the [Google Cloud Console](https://console.cloud.google.com/).
2. Create a new project (or pick an existing one) using the project picker in the
   top bar.

## 2. Enable the Gmail API

1. Go to **APIs & Services → Library**.
2. Search for **Gmail API** and click **Enable**.

## 3. Configure the OAuth consent screen

1. Go to **APIs & Services → OAuth consent screen**.
2. Choose **External** (unless your account is part of a Google Workspace org, in
   which case **Internal** is simpler) and fill in the required app name and contact
   email.
3. On the **Scopes** step you can leave it empty — renomail requests its scope at
   run time.
4. While the app is in **Testing** mode, add your Gmail address(es) under **Test
   users**. (Tokens for a Testing-mode app expire periodically; re-run
   `renomail auth <account>` if Gmail sync starts failing with an auth error.)

## 4. Create the OAuth client (Desktop app)

1. Go to **APIs & Services → Credentials**.
2. Click **Create credentials → OAuth client ID**.
3. Set **Application type** to **Desktop app**, give it a name, and click **Create**.
4. **Download JSON** for the new client.

## 5. Install the credentials

Move the downloaded file to renomail's config directory as `credentials.json`:

```sh
mkdir -p ~/.config/renomail
mv ~/Downloads/client_secret_*.json ~/.config/renomail/credentials.json
```

(If `XDG_CONFIG_HOME` is set, use `$XDG_CONFIG_HOME/renomail/credentials.json`.)

## 6. Authorize each account

```sh
renomail auth me@gmail.com
```

What happens:

1. renomail binds a temporary `127.0.0.1` loopback listener and builds a consent
   URL bound to a random `state` (CSRF protection).
2. It prints the URL **and** opens it in your default browser. On a headless/SSH
   box where no browser can launch, copy the printed URL into a browser yourself.
3. Approve the read-only access. Google redirects back to the loopback listener,
   renomail exchanges the code for a token, and saves it at
   `~/.config/renomail/token-<account>.json` (mode `0600`).

The saved token includes a refresh token, so subsequent `renomail` runs refresh
access silently — you only run `auth` again if you revoke access or the token
expires. Repeat this step for each `[[gmail]]` account in your config.

## Feeds (OPML)

renomail imports feeds from one or more OPML files (the export format every feed
reader supports):

1. Export OPML from your current reader (e.g. *Settings → Export → OPML*).
2. Save it somewhere, e.g. `~/feeds.opml`.
3. Reference it in `config.toml`:

   ```toml
   [[opml]]
   path = "~/feeds.opml"
   ```

You can list several `[[opml]]` blocks, and/or add individual feeds with
`[[feed]]`. See [CONFIG.md](CONFIG.md) for the full reference.

## Verify

```sh
renomail dump   # prints the fetched/cached feed to stdout — a quick sanity check
renomail        # launch the TUI
```

If a Gmail account is not yet authorized, renomail degrades gracefully: it shows a
warning on the status line prompting you to run `renomail auth <account>` and keeps
serving the other sources.
