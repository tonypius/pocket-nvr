#!/system/bin/sh
# PocketNVR boot module: only bootstraps boot-time startup. Binaries and
# config are installed under /data/nvr by scripts/deploy.sh.
ui_print "- PocketNVR boot module installed"
ui_print "  binaries/config live in /data/nvr (scripts/deploy.sh)"
ui_print "  daemons start on next reboot or: nvrctl start"
set_perm_recursive "$MODDIR" 0 0 0755 0644
