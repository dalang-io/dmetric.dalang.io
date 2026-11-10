#!/usr/bin/env bash
# Manual release build. No CI: run this on your machine, then upload.
#
#   make release                 # builds the version in Makefile
#   scripts/release.sh 2026.11.2 # must match Makefile, mygo.json and model.go
#
# Output: dist/release/v<VERSION>/
#   dmetric-{darwin,linux,windows}-{amd64,arm64}[.exe]   standalone binaries (used by in-app update)
#   dmetric-<VERSION>-{arm64,amd64}.dmg                  macOS disk images (macOS host required)
#   SHA256SUMS                                           checksums of everything above
#   RELEASE_NOTES.md                                     rendered from internal/server/ui/releases.json
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "release: $*" >&2; exit 1; }

VERSION="${1:-$(sed -n 's/^VERSION ?= *//p' Makefile | head -1)}"
[[ "$VERSION" =~ ^20[0-9]{2}\.[0-9]+\.[0-9]+$ ]] || die "versi '$VERSION' harus berbentuk YYYY.M.P (mis. 2026.11.2)"

# The version lives in three files; a mismatch silently ships a wrong DMG.
mk=$(sed -n 's/^VERSION ?= *//p' Makefile | head -1)
mygo=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json | head -1)
src=$(sed -n 's/^var Version = "\([^"]*\)".*/\1/p' internal/model/model.go | head -1)
for pair in "Makefile:$mk" "mygo.json:$mygo" "internal/model/model.go:$src"; do
  [ "${pair#*:}" = "$VERSION" ] || die "${pair%%:*} berisi '${pair#*:}', bukan $VERSION — jalankan scripts/bump-version.sh $VERSION"
done

# Notes must be final before anything is built.
go run ./tools/relnotes -version "$VERSION" -check

if [ "${SKIP_TESTS:-}" != 1 ]; then
  echo "==> go vet + go test"
  go vet ./...
  go test ./...
fi

OUT="dist/release/v$VERSION"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> binaries"
LDFLAGS="-s -w -X dmetric.dalang.io/internal/model.Version=$VERSION"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/arm64 windows/amd64; do
  os=${target%/*}; arch=${target#*/}; ext=""
  [ "$os" = windows ] && ext=".exe"
  echo "    $os/$arch"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$LDFLAGS" -o "$OUT/dmetric-$os-$arch$ext" .
done

echo "==> macOS disk images"
if [ "$(uname)" = Darwin ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  go tool mygo build -platform darwin/arm64,darwin/amd64 -o "$tmp"
  found=0
  while IFS= read -r dmg; do
    cp "$dmg" "$OUT/"; found=$((found + 1))
  done < <(find "$tmp" -name '*.dmg')
  [ "$found" -ge 2 ] || die "mygo hanya menghasilkan $found DMG (butuh arm64 dan amd64)"
else
  echo "    dilewati: DMG hanya bisa dibuat di macOS" >&2
fi

echo "==> checksums + notes"
(
  cd "$OUT"
  if command -v sha256sum >/dev/null; then sha256sum dmetric-* > SHA256SUMS; else shasum -a 256 dmetric-* > SHA256SUMS; fi
)
go run ./tools/relnotes -version "$VERSION" > "$OUT/RELEASE_NOTES.md"

# The binary for this machine must report the version it was built as.
host="dmetric-$(go env GOOS)-$(go env GOARCH)"
if [ -x "$OUT/$host" ]; then
  got=$("$OUT/$host" -version)
  [ "$got" = "DMetric $VERSION" ] || die "$host melaporkan '$got', diharapkan 'DMetric $VERSION'"
fi

echo
ls -lh "$OUT"
cat <<MSG

Siap di $OUT. Belum ada yang diunggah. Saat rilis:

  git tag v$VERSION && git push origin v$VERSION     # catatan: workflow CI (jika masih aktif) ikut terpicu oleh tag
  gh release create v$VERSION "$OUT"/dmetric-* "$OUT"/SHA256SUMS \\
     --title "v$VERSION" --notes-file "$OUT/RELEASE_NOTES.md"

Update dalam aplikasi mencari asset 'dmetric-<os>-<arch>[.exe]' dan SHA256SUMS.
MSG
