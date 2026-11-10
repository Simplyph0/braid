#!/usr/bin/env bash
# Braid demo: a hub and two "PCs" (Desktop, Laptop) on this machine, using
# the builds in dist/ (run ./build.sh first). Everything lives in ~/braid-demo.
set -e
R=$(cd "$(dirname "$0")/.." && pwd)
B=$R/dist/braid
H=$R/dist/braid-hub
D=$HOME/braid-demo
pkill -f "braid-hub -data $D" 2>/dev/null || true
pkill -f "$B run" 2>/dev/null || true
rm -rf "$D" && mkdir -p "$D"
"$H" -data "$D/hub" -addr 127.0.0.1:8490 >"$D/hub.log" 2>&1 &
for i in $(seq 40); do curl -s -o /dev/null http://127.0.0.1:8490/ && break; sleep 0.5; done
for pc in Desktop Laptop; do
  export BRAID_DIR="$D/config-$pc"
  BRAID_PASSPHRASE=demo-passphrase "$B" setup http://127.0.0.1:8490 >/dev/null
  sed -i "s/\"pc\": \".*\"/\"pc\": \"$pc\"/" "$BRAID_DIR/config.json"
  "$B" add "$D/$pc/project" demo >/dev/null
  # each pretend PC gets its own Claude chats folder
  CLAUDE_CONFIG_DIR="$D/claude-$pc" "$B" run 2>&1 | sed "s/^/[$pc] /" &
done
echo "hello from the desktop" > "$D/Desktop/project/notes.txt"
cat <<MSG

Braid demo is running: a hub and two PCs, Desktop and Laptop.

  Desktop folder: $D/Desktop/project     its Claude chats: $D/claude-Desktop/projects
  Laptop folder:  $D/Laptop/project      its Claude chats: $D/claude-Laptop/projects

Web app: http://127.0.0.1:8490  (passphrase: demo-passphrase)

Try it (in another terminal):
  - Edit or add files in one folder; about 5-10 seconds later the other matches.
  - See status:  BRAID_DIR=$D/config-Laptop $B status
  - See history: BRAID_DIR=$D/config-Laptop $B log demo

Press Ctrl-C here to stop everything. What the two PCs do shows below.

MSG
trap 'kill $(jobs -p) 2>/dev/null; exit 0' INT TERM
wait
