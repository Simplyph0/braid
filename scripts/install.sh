#!/bin/sh
# Installs braid (the program for each PC) on any Linux: one static file,
# no dependencies. Usage: curl -fsSL <this script's URL> | sh
# BRAID_INSTALL_DIR picks where it goes (default ~/.local/bin).
set -eu
REPO=Simplyph0/braid
[ "$(uname -s)" = Linux ] || { echo "This installer is for Linux. Windows: see INSTALL.md." >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7*|armv6*|armhf) ARCH=arm ;;
  *) echo "No braid build for $(uname -m) yet." >&2; exit 1 ;;
esac
DIR=${BRAID_INSTALL_DIR:-$HOME/.local/bin}
BASE=${BRAID_BASE_URL:-https://github.com/$REPO/releases/latest/download}
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else echo "Needs curl or wget." >&2; exit 1; fi
}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
echo "Downloading braid for linux-$ARCH…"
fetch "$BASE/braid-linux-$ARCH" "$TMP/braid"
fetch "$BASE/manifest.json" "$TMP/manifest.json"
want=$(tr -d ' \n' < "$TMP/manifest.json" | sed -n "s/.*\"linux-$ARCH\":{\"name\":\"[^\"]*\",\"sha256\":\"\([0-9a-f]*\)\".*/\1/p")
if command -v sha256sum >/dev/null 2>&1 && [ -n "$want" ]; then
  got=$(sha256sum "$TMP/braid" | cut -d' ' -f1)
  [ "$got" = "$want" ] || { echo "Checksum mismatch: the download is damaged. Nothing installed." >&2; exit 1; }
fi
mkdir -p "$DIR"
chmod +x "$TMP/braid"
mv "$TMP/braid" "$DIR/braid"
echo "Installed $("$DIR/braid" version) to $DIR/braid"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "Note: $DIR isn't on your PATH. Add it, or run $DIR/braid." ;; esac
cat <<MSG

Next:
  braid setup <hub-url>            connect to your hub (it asks for your passphrase)
  braid add ~/Projects/my-project  keep a folder in its timeline
  braid install                    start Braid when you log in
MSG
