#!/system/bin/sh
# PocketNVR Magisk service.sh — COMP-1 (FR-SUP-1/2/5).
# Boots the appliance after every reboot: waits for boot + network,
# acquires the wakelock, regenerates runtime configs from config.yaml,
# starts all daemons in dependency order, then runs the watchdog loop.
# Phase 5 adds restart backoff + log caps inside `nvrctl watch`.

MODDIR=${0%/*}
BASE=/data/nvr
LOG=$BASE/logs

log() { echo "$(date '+%F %T') service.sh: $*" >>"$LOG/supervisor.log"; }

mkdir -p "$BASE" "$LOG" "$BASE/run" "$BASE/recordings" "$BASE/snapshots" "$BASE/clips" "$BASE/models"

# A reboot or dead battery leaves the run dir behind on /data: stale
# pidfiles whose PIDs Android has since recycled would make nvrctl think
# the daemons are already alive (blinding start and the watchdog), and
# stale backoff counters could delay recovery by up to 10 min. Nothing of
# ours runs this early in boot, so it is safe to wipe.
rm -f "$BASE"/run/*.pid "$BASE"/run/*.att "$BASE"/run/*.next

log "waiting for boot_completed"
i=0
while [ "$(getprop sys.boot_completed)" != "1" ] && [ $i -lt 60 ]; do
	sleep 2
	i=$((i + 1))
done

# Network-not-ready tolerance (FR-SUP-5, FLAG-9): wait up to 120 s for a
# default route — note Android keeps it in a per-network table, not the
# main table — then proceed regardless; daemons retry internally.
log "waiting for network"
i=0
until ip route show table all 2>/dev/null | grep -q default || [ $i -ge 60 ]; do
	sleep 2
	i=$((i + 1))
done
ip route show table all 2>/dev/null | grep -q default &&
	log "network is up" || log "network wait timed out; starting anyway (daemons retry)"

# Persistent CPU wakelock (FR-SUP-2). Released by nvrctl stop / reboot.
if grep -q "wakelock: true" "$BASE/config.yaml" 2>/dev/null; then
	echo pocketnvr > /sys/power/wake_lock 2>/dev/null &&
		log "wakelock acquired" || log "wakelock FAILED (check kernel)"
fi

# Regenerate runtime configs from the single source of truth (FR-CFG-1).
"$BASE/bin/nvrd" -config "$BASE" -mediamtx-config "$BASE/mediamtx.yml" >>"$LOG/supervisor.log" 2>&1
"$BASE/bin/nvrd" -config "$BASE" -detector-config "$BASE/detector.json" >>"$LOG/supervisor.log" 2>&1
chmod 600 "$BASE/mediamtx.yml" "$BASE/detector.json" 2>/dev/null

log "starting daemons + watchdog"
"$BASE/bin/nvrctl" start
nohup "$BASE/bin/nvrctl" watch >>"$LOG/watchdog.log" 2>&1 &
log "boot sequence done"
