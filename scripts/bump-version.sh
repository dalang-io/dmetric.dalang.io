#!/usr/bin/env bash
# Sets the version in the three places that must agree.
#   scripts/bump-version.sh 2026.11.2
set -euo pipefail
cd "$(dirname "$0")/.."
V="${1:-}"
[[ "$V" =~ ^20[0-9]{2}\.[0-9]+\.[0-9]+$ ]] || { echo "usage: $0 YYYY.M.P" >&2; exit 1; }
sed -i.bak -E "s/^(VERSION \?= ).*/\1$V/" Makefile
sed -i.bak -E "s/(\"version\": *\")[^\"]*(\")/\1$V\2/" mygo.json
sed -i.bak -E "s/^(var Version = \")[^\"]*(\")/\1$V\2/" internal/model/model.go
rm -f Makefile.bak mygo.json.bak internal/model/model.go.bak
echo "versi diset ke $V di Makefile, mygo.json, internal/model/model.go"
echo "Berikutnya: set \"released\": true dan tanggal untuk $V di internal/server/ui/releases.json."
