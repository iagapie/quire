#!/bin/sh

# Unload the AppArmor profile installed with the package (postinstall loads it).
if [ -f /etc/apparmor.d/quire ] && [ -d /sys/kernel/security/apparmor ] && command -v apparmor_parser >/dev/null 2>&1; then
  apparmor_parser -R /etc/apparmor.d/quire >/dev/null 2>&1 || true
fi

exit 0
