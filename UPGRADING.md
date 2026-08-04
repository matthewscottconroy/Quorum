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
