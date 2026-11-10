# Braid

Work on a project from any of your PCs without ever merging folders by hand.

A project is **one timeline**, not a folder copied to every PC. Every PC
records its changes into the timeline automatically and its folder shows a
moment of it (normally the newest). Work done apart becomes two strands, and
Braid joins them by itself. Nothing is ever overwritten without being kept in
the timeline, and nothing stops to ask you a question.

Braid succeeds [ClaudeSync](https://github.com/Simplyph0/claudesync).

## Status

Early development. Done: the timeline core (milestone 1), the PC agent
(milestone 2), most of braiding (milestone 3), Claude merges by intent
(milestone 4), and the hub with its web app (milestone 5, without automatic
updates yet).

| | |
|---|---|
| Moments, strands, record-before-replace, jump, restore | done |
| `.git` travels with the project; braided git branches are kept | done |
| Braiding: fast-forward, different files, different lines, newest wins for binaries | done |
| Overlapping edits: wait on the PC's own strand until a merger joins them | done |
| Claude merge by intent, with an optional check command and one retry | done |
| Moments record what you asked Claude in that folder | done |
| Agent on each PC (`braid run`), CLI, Linux autostart | done |
| Hub web app: projects, PCs, timeline, what changed, restore, show a moment on any PC, make a PC match another | done |
| Hub as a container (`Dockerfile`) | done |
| Automatic updates for PCs and hub | next |

## How it works

- **Moment:** the whole project folder (including `.git`) at one point in
  time, end-to-end encrypted. Unchanged files are stored once.
- **main:** the strand every PC follows. A PC that worked offline gets its
  own strand (`pc/<name>`) until its work is braided in.
- **Braid order:** fast-forward → different files → different lines of the
  same text file → Claude merges by intent → else the PC's work waits on its
  strand (never lost, never mixed).
- **Safety:** a folder is recorded before anything changes it; a missing
  folder is never treated as deleted; files being edited or locked by git are
  left alone; paths from the hub are checked before anything is written.

The full blueprint: <https://claude.ai/artifact/QwbmgLhKSd5nea4ByjY8N7> (private doc).

## Try it

On the server:

```bash
braid-hub -data /var/lib/braid -addr :8490
```

It prints the `braid setup` line to run on each PC, and serves the web app
on the same address: sign in with the hub token and your passphrase. The web
app decrypts everything inside your browser (it runs Braid's own code as
WebAssembly); the hub still only ever sees encrypted data.

Or as a container (for Cockpit's Podman): `podman build -t braid-hub .`, then
run it with a volume on `/data` and port 8490. Then on each PC:

```bash
braid setup http://<server>:8490 <token>
braid add ~/Projects/PixelBit
braid install
```

`braid status`, `braid log <name>`, `braid jump`, `braid follow` and
`braid restore` steer it.

When two PCs changed the same lines, Braid asks Claude Code on the PC that
needs the result to join them (`claude -p`, no tools: it only sees that file
and can only answer with the merged text). Claude Code must be signed in on
that PC. To have every Claude merge checked first:

```bash
braid check PixelBit "go build ./..."
```

If the check fails, Claude gets its output and one more try; if it still
fails, that PC's work waits on its own strand and nothing is mixed. Builds are static: nothing to install, and they run
on old Linux (Debian 12) as well as new.

## Build and test

```bash
./build.sh      # the web app's engine, then braid and braid-hub, into dist/
go test ./...
```
