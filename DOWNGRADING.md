# Downgrading a Successful-but-Unwanted Upgrade

UPGRADING.md covers upgrades that *fail* — those roll back automatically.
This document covers the other case: the upgrade **worked**, the app runs,
but you want the previous version anyway (a feature landed wrong, members
are confused, you want time to think).

## The three layers, and which ones moved

An upgrade can change up to three things. Downgrading means walking back
exactly the ones that moved — no more.

| Layer | Moved when… | Walked back by |
|---|---|---|
| **Binary/UI** | always | `sudo ops/upgrade.sh <old-ref>` |
| **Schema** | the release added migrations | `quorum -migrate-down N` (or restore) |
| **Data** | members used the new features | nothing — data in new tables dies with them |

**First, find out if the schema moved.** On the server:

```
cd /opt/quorum
sudo -u quorum git log --oneline <old-sha>..HEAD -- internal/db/migrations | cat
```

No output → **Case 1**. Output listing `NNNN_*.sql` files → **Case 2**, and
the highest `NNNN` before the upgrade is your rollback target.

## Case 1 — no schema change (the easy, common case)

```
sudo ops/upgrade.sh <old-sha-or-tag>
```

That's it: same safety net as an upgrade (backup, build, atomic swap,
/readyz poll, auto-restore). Tell members to hard-refresh (Ctrl+Shift+R).

## Case 2 — the release carried migrations

Read this paragraph before typing anything: **down-migrations DROP what the
up-migrations built.** Downgrading past, say, the discussions migration
deletes every channel and message members created since. That is not a
malfunction — it is what "the database no longer has those tables" means.
Decide with eyes open, and take the backup.

```
# 1. Freeze and back up (non-negotiable)
sudo systemctl stop quorum
cd /opt/quorum && sudo make backup

# 2. Walk the schema back — with the CURRENT (new) binary, which is the
#    one that carries these migrations' .down.sql files.
#    Target = highest migration number that existed BEFORE the upgrade.
sudo -u quorum /opt/quorum/quorum -migrate-down <target>

# 3. Now downgrade the code (this also restarts the service)
sudo ops/upgrade.sh <old-sha-or-tag>
```

Verify: `curl -s localhost:8080/readyz`, then in the app confirm the
unwanted feature is gone and everything else is intact.

### The alternative: restore instead of migrate-down

`scripts/backup.sh restore <pre-upgrade-dump>` returns the **entire
database** to the moment before the upgrade. Choose it over migrate-down
when the new version has been live so briefly that losing *everything*
since (all tables, not just the new ones) is acceptable — or when you
suspect the new version wrote bad data into old tables. Otherwise prefer
migrate-down: it surgically removes only the new feature's tables while
keeping every dues payment, minute, and message members created in the
old tables since the upgrade.

| | migrate-down | restore backup |
|---|---|---|
| Loses new-feature data | yes (inherent) | yes |
| Loses unrelated data created since upgrade | **no** | **yes** |
| Depends on | tested .down.sql (CI proves every one) | tested backups (weekly verify proves them) |

## After any downgrade

- Members: hard-refresh; announce which feature was withdrawn and why.
- Pin the server there deliberately — the next plain `sudo ops/upgrade.sh`
  will happily re-upgrade to `origin/main`. If main itself is wrong,
  revert the offending commits on main (`git revert`) rather than living
  on a detached checkout forever.
- Note what happened in your operator log; if the downgrade crossed
  migrations, record which data was forfeited.

## Rehearsal

Like disaster recovery, a downgrade is calmest the second time. Once, on a
throwaway instance (or the local stack): upgrade, create a little data in
the new feature, then walk both paths. Twenty minutes, and this document
stops being theory.
