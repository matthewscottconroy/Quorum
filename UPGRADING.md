# Upgrading a Running Deployment

How to move the production server to newer code after changes land on
GitHub. Written for the single-instance deployment (DEPLOY-EC2.md); every
step says where you type it and what you should see.

## The mental model (30 seconds)

Your server holds a **git checkout** of the GitHub repository plus a
**compiled binary** built from it. Upgrading is: fetch the new code, build a
new binary, swap it in, restart. Two facts make this boring in the good way:

- **Migrations are embedded in the binary** and apply themselves at startup,
  safely (one at a time, under a database lock). There is no separate
  "migrate" step to forget — *a deploy is the migration*.
- The upgrade script **keeps the previous binary** and **takes a database
  backup first**, so both kinds of undo exist before anything changes.

## Step 0 — only upgrade green code

1. In your browser: your repository on GitHub → **Actions** tab.
2. The newest run on `main` must show a **green check**. That means the
   linter, both test suites, and the vulnerability scan all passed.
3. Red X or still-spinning? Don't upgrade. The server doesn't re-run tests —
   CI is where code earns the right to be deployed.

## Path A — on the server (the normal path)

Works from any SSH or SSM session; nothing is needed on your own machine.

1. Connect:
   ```
   ssh -i ~/.ssh/quorum-key.pem rocky@YOUR-SERVER-IP
   ```
2. Run the upgrade:
   ```
   cd /opt/quorum
   sudo ops/upgrade.sh
   ```

You see, in order: `fetching from GitHub` · the list of incoming commits ·
`pre-upgrade database backup` (a line naming the new `.pgdump.enc` file) ·
`building` (a pause — first run after a toolchain bump also downloads Go) ·
`restarting quorum.service` · and finally:

```
upgrade ok — running <version>, /readyz is green
```

That last line is the whole point. If the new version fails to start, the
script **puts the old binary back by itself**, restarts, prints the error
log, and tells you what to look at — the site stays up on the previous
version while you investigate.

Members' experience during an upgrade: a one-to-two-second blip. Nobody is
signed out.

## Path B — from a workstation (optional)

If the machine you're sitting at has Go installed and a checkout of the
repo, you can push an upgrade instead of pulling it:

```
ops/deploy.sh rocky@YOUR-SERVER-IP
```

Same safety net (atomic swap, `/readyz` poll, auto-rollback), but it builds
locally and ships **your checkout as it sits** — which is also its caveat:
it deploys what you have, not what GitHub has. Prefer Path A unless you're
deliberately testing something unpushed.

## Rolling back to an older version on purpose

The same script, pointed at any tag or commit from the GitHub history:

```
sudo ops/upgrade.sh a1b2c3d        # a commit hash
sudo ops/upgrade.sh v1.2.0         # or a tag, once you start tagging
```

One warning for going *backwards*: if the newer version had already applied
a schema migration, the older binary expects the older schema — follow
**RUNBOOK.md → "Roll back a bad migration"** first (the pre-upgrade backup
the script took is your safety line).

## The arcade cartridge (the one non-Go build artifact)

The Top Secret arcade is a WebAssembly "cartridge" built from Rust. It is
**not in the git repository** (`web/arcade/` is gitignored) and the server
never builds it — so it follows different rules than everything else:

- The binary **embeds whatever sits in `web/arcade/` at build time**. The
  upgrade script's `go build` on the server re-embeds the copy already in
  `/opt/quorum/web/arcade` on every upgrade. Because git ignores that
  directory, fetches and fast-forwards never touch it: **install the
  cartridge once and every future upgrade carries it forward
  automatically.** You do NOT repeat the cartridge steps per upgrade.
- No cartridge is fine: the app runs normally and the arcade page shows
  "CARTRIDGE NOT INSTALLED" on each cabinet.
- You only redo the steps below when a release **changes the games
  themselves** (the release notes / commit log will say so — look for
  changes under `arcade/`).

### Installing or refreshing the cartridge

On any machine with Rust (your workstation, not the server):

1. One-time toolchain setup:
   ```
   rustup target add wasm32-unknown-unknown
   cargo install wasm-bindgen-cli --version 0.2.126
   ```
2. In your checkout of the repo, at the same commit you're deploying:
   ```
   make arcade
   ```
   You see `cargo build` compile for a while, then `wasm-bindgen`, then
   (if you have binaryen installed) `wasm-opt`. It ends by listing
   `web/arcade/` — three files: `arcade.js`, `arcade_bg.wasm`, and a
   license file. The `.wasm` is ~10-14 MB; that's normal (it travels
   gzipped at ~4 MB).
3. Copy the directory to the server:
   ```
   scp -i ~/.ssh/YOUR-KEY.pem -r web/arcade rocky@YOUR-SERVER-IP:/tmp/arcade
   ssh -i ~/.ssh/YOUR-KEY.pem rocky@YOUR-SERVER-IP
   sudo rm -rf /opt/quorum/web/arcade
   sudo mv /tmp/arcade /opt/quorum/web/arcade
   sudo chown -R quorum:quorum /opt/quorum/web/arcade
   ```
4. Rebuild and restart so the new cartridge is embedded — the normal
   upgrade does this, or if you're already at the newest commit force a
   rebuild by re-running the deploy path you use:
   ```
   cd /opt/quorum && sudo ops/upgrade.sh
   ```
   (If it prints `already at <sha> — nothing to do`, run
   `sudo -u quorum go build -o quorum.next ./cmd/quorum && sudo mv quorum.next quorum && sudo restorecon quorum && sudo systemctl restart quorum`
   — that's the script's build-and-swap, minus the git steps.)
5. Verify in the browser: Top Secret → any cabinet → it boots after
   INSERT CREDIT instead of saying "CARTRIDGE NOT INSTALLED".

`ops/upgrade.sh` prints whether it found a cartridge at build time
(`arcade cartridge found — it will be embedded` or a note that the arcade
will report "cartridge not installed"), so a missing or forgotten
cartridge is visible in the upgrade output, not a surprise later.

### Turning the arcade off without touching any of this

Admins can hide and disable the arcade entirely from **Settings →
Organization settings → Top Secret arcade** — no rebuild, takes effect in
about 30 seconds, keeps all plays/scores/levels. That's the right knob if
the games should go away; the cartridge steps above are only about how the
games' code ships.

## If something goes wrong anyway

| Symptom | Do |
|---|---|
| Script ended with `restoring previous binary` | Site is fine on the old version. Read the printed log lines; fix or report the error before retrying. |
| Upgrade succeeded but behavior is wrong | `sudo ops/upgrade.sh <previous-commit>` (see rollback warning above). |
| `git` complains about local changes | Someone edited files on the server. `sudo -u quorum git -C /opt/quorum status` to see what; server checkouts should never be hand-edited — fix it in GitHub instead. |
| Build fails with a Go version error | The repo pins its toolchain and downloads it automatically; the server just needs *a* Go: `sudo dnf install golang`. |

## The habit

Upgrades are cheapest taken small and often: merge to GitHub → Actions green
→ `sudo ops/upgrade.sh` → read the one-line verdict. Five minutes, and the
gap between "what's deployed" and "what's in main" never grows large enough
to be scary.
