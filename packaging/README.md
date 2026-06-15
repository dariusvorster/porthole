# Packaging

Two artifacts for distributing `portholed`:

- **`app.porthole.portholed.plist`** — a launchd **LaunchAgent** for running
  portholed yourself (the non-Homebrew path).
- **`porthole.rb`** — the Homebrew formula (destined for the `homebrew-porthole`
  tap). Brew users get the background service for free via `brew services` and do
  **not** need the plist below.

## Running via the LaunchAgent (non-brew)

The agent runs as **your user**, not root — portholed shells the per-user
`container` CLI and writes its SQLite store under `~/Library/Application Support`.

1. Edit the plist: replace `USERNAME` with your short user name (`id -un`), and
   create the log directory:
   ```sh
   mkdir -p ~/Library/Logs/porthole
   ```
2. Copy it into place and load it:
   ```sh
   cp app.porthole.portholed.plist ~/Library/LaunchAgents/
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/app.porthole.portholed.plist
   ```
3. Open the console: <http://127.0.0.1:9191>

To stop / unload:
```sh
launchctl bootout gui/$(id -u)/app.porthole.portholed
```

The paths assume a Homebrew Apple-silicon install (`/opt/homebrew/bin/portholed`)
and the Apple `container` CLI at `/usr/local/bin/container`. Adjust if yours
differ.

## Homebrew (recommended)

```sh
brew tap <you>/porthole
brew install porthole
brew services start porthole
```

`brew services` installs and manages the LaunchAgent for you (logs land under the
Homebrew prefix, `$(brew --prefix)/var/log/porthole/`); the standalone plist above
is only for non-brew installs.
