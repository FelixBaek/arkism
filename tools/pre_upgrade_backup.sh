#!/bin/sh
# Before an upgrade: one encrypted backup now, and a non-zero exit when none appears.
# The daily backup unit exits 0 without a bundle while another backup holds its lock,
# so the bundle count is the proof. Then the oldest fresh bundles beyond
# ARKISM_BACKUP_KEEP (default 10) go, so repeated upgrades cannot fill the disk.
# Usage (as root): tools/pre_upgrade_backup.sh && <replace the programs>
set -eu
root="${ARKISM_ROOT:-/srv/arkism}"
keep="${ARKISM_BACKUP_KEEP:-10}"
bundles() { find "$root/backups" -maxdepth 1 -name 'arkism-prod-*.cms' | wc -l; }
before=$(bundles)
systemctl start "${ARKISM_BACKUP_UNIT:-arkism-backup.service}"
after=$(bundles)
if [ "$after" -le "$before" ]; then
    echo "no new backup bundle in $root/backups ($before before, $after after): stop the upgrade" >&2
    exit 1
fi
flock --nonblock "${ARKISM_BACKUP_LOCK:-/run/lock/arkism-backup.lock}" \
    "$root/bin/arkism" backup-retention --directory "$root/backups" --execute --keep "$keep"
