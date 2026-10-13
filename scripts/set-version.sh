#!/bin/sh
# Sets the application version in every file that carries it (see "Versioning" in the
# README). Usage: scripts/set-version.sh 0.0.2
# Uses perl instead of sed -i, whose syntax differs between macOS and GNU.
set -eu

NEW=${1:?usage: $0 <version>}
cd "$(dirname "$0")/.."
OLD=$(perl -ne 'print $1 if /^\s+version:\s*"([^"]+)"/' build/config.yml)
[ -n "$OLD" ] || { echo "no version in build/config.yml" >&2; exit 1; }
[ "$OLD" != "$NEW" ] || exit 0
# Windows version resources take numbers only: 0.1.0-rc.1 is 0.1.0 there.
WIN=${NEW%%-*} OLDWIN=${OLD%%-*}
export OLD NEW WIN OLDWIN

perl -pi -e 's/"\Q$ENV{OLD}\E"/"$ENV{NEW}"/' build/config.yml build/linux/nfpm/nfpm.yaml
perl -pi -e 's/"\Q$ENV{OLDWIN}\E"/"$ENV{WIN}"/' build/windows/info.json build/windows/nsis/wails_tools.nsh
perl -pi -e 's/version="\Q$ENV{OLDWIN}\E"/version="$ENV{WIN}"/' build/windows/wails.exe.manifest
perl -pi -e 's#<string>\Q$ENV{OLD}\E</string>#<string>$ENV{NEW}</string>#g' \
	build/darwin/Info.plist build/darwin/Info.dev.plist
(cd frontend && npm version "$NEW" --no-git-tag-version --allow-same-version >/dev/null)

echo "version $OLD -> $NEW"
