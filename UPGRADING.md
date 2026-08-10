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
**not in the git repository** (`web/arcade/` is gitignored), so it follows
different rules than everything else:

- The binary **embeds whatever sits in `web/arcade/` at build time**, and
  because git ignores that directory, fetches and fast-forwards never
  touch it — a cartridge in `/opt/quorum/web/arcade` is carried forward
  by every upgrade automatically.
- Once the server has a Rust toolchain (one-time setup below), the
  upgrade script **rebuilds the cartridge by itself** whenever an upgrade
  changes files under `arcade/`. There is no per-upgrade ritual.
- No cartridge is fine: the app runs normally and the arcade page shows
  "CARTRIDGE NOT INSTALLED" on each cabinet.

### Building on the server (the hands-off path)

Give the server a Rust toolchain **once** and `ops/upgrade.sh` takes care
of the rest forever: on every upgrade it checks whether the release
touched `arcade/` (or whether no cartridge exists yet) and rebuilds the
cartridge itself before building the binary. A cartridge build failure
never blocks the app upgrade — the script keeps the previous cartridge
and says so loudly.

**Check the machine first.** The Rust build needs ~2–3 GB of memory at
its peak and a few GB of disk (toolchain ~1.5 GB, build directory 2–4 GB).

```
free -h        # "Mem: total" — if under 4 GB, add swap (next block)
df -h /opt     # want several GB free
```

If memory is under ~4 GB, add a one-time swap file so the first build
can't be killed mid-link:

```
sudo fallocate -l 4G /swapfile && sudo chmod 600 /swapfile
sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap defaults 0 0' | sudo tee -a /etc/fstab
```

**One-time toolchain install**, as the `quorum` user (its home IS
`/opt/quorum`, so the toolchain lands inside the deploy dir — the repo's
`.gitignore` already covers `.cargo/` and `.rustup/`, keeping
`git status` clean):

```
sudo -u quorum -H bash
cd /opt/quorum
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal
. .cargo/env
rustup target add wasm32-unknown-unknown
cargo install wasm-bindgen-cli --version 0.2.126
exit
```

Expected: rustup prints `Rust is installed now`; the target add is
seconds; `cargo install wasm-bindgen-cli` **compiles it from source and
takes several minutes** on a small instance — that's normal. The
`--version 0.2.126` pin matters: it must match the `wasm-bindgen` pin in
`arcade/cart/Cargo.toml`, and the build fails with a clear version
message if they drift.

Optional: `wasm-opt` (from binaryen) shrinks the cartridge ~40%. It is
**not in Rocky's stock repos** and may be missing from EPEL on newer
releases; try `sudo dnf install epel-release && sudo dnf install
binaryen`, and if that says "no match", install the project's prebuilt
binary instead:

```
cd /tmp
curl -LO https://github.com/WebAssembly/binaryen/releases/download/version_119/binaryen-version_119-x86_64-linux.tar.gz
tar xzf binaryen-version_119-x86_64-linux.tar.gz
sudo install -m 755 binaryen-version_119/bin/wasm-opt /usr/local/bin/wasm-opt
wasm-opt --version    # → "wasm-opt version 119"
```

Or skip it entirely — the only consequence is a larger one-time
download for members (~6-7 MB gzipped instead of ~4 MB).

From then on, upgrades just work. In `sudo ops/upgrade.sh` output you'll
see one of:

```
==> building arcade cartridge (Rust → wasm; first build takes a while)
```

(the **first build is the slow one** — 15–30 minutes on a 2-vCPU box;
later ones reuse the cached build directory and take a minute or two), or

```
==> arcade sources unchanged — keeping existing cartridge
```

which is the usual case and costs nothing. Verify after the first build:
Top Secret → any cabinet boots after INSERT CREDIT instead of saying
"CARTRIDGE NOT INSTALLED".

### Alternative: build elsewhere, copy in

If the server is too small to compile Rust, build on any machine that
isn't (same one-time toolchain setup as above, minus the swap), then:

1. In your checkout, at the commit you're deploying: `make arcade` —
   ends by listing `web/arcade/` (`arcade.js`, `arcade_bg.wasm` ~10-14 MB;
   it travels gzipped at ~4 MB).
2. Copy it over and rebuild:
   ```
   scp -i ~/.ssh/YOUR-KEY.pem -r web/arcade rocky@YOUR-SERVER-IP:/tmp/arcade
   ssh -i ~/.ssh/YOUR-KEY.pem rocky@YOUR-SERVER-IP
   sudo rm -rf /opt/quorum/web/arcade && sudo mv /tmp/arcade /opt/quorum/web/arcade
   sudo chown -R quorum:quorum /opt/quorum/web/arcade
   cd /opt/quorum && sudo ops/upgrade.sh
   ```

Without a server toolchain, `ops/upgrade.sh` still tells you where you
stand each run: `arcade cartridge found — it will be embedded`, or a note
that cabinets will say "cartridge not installed".

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
