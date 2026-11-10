# Braid

**Work on the same project from all your computers, without ever merging by hand.**

Braid keeps each project as one shared, end-to-end encrypted timeline. Every
computer records its changes into it automatically and shows the newest
version. Work done on two computers at once is joined for you, and nothing is
ever overwritten without being kept in the timeline.

- **No sync questions.** Changes to different files or different lines are
  joined automatically. Where both computers changed the same lines, Claude
  Code (if installed) merges them by what each side was doing; otherwise
  that computer's work waits, untouched, until it can be joined.
- **Git included.** `.git` travels with the project, so every computer has the
  same branch, commits and uncommitted work.
- **Everything is undoable.** Show any earlier moment on any computer, or
  restore it for all of them, from the command line or the web app.
- **Claude chats come along.** Claude Code conversations for a project follow
  it between computers.
- **Your server, your keys.** The hub runs on your own server and only ever
  stores encrypted data. The web app decrypts in your browser.
- **Updates itself.** Computers install new signed builds from your hub.

Linux (x86-64, ARM64, ARMv7) and Windows (x86-64, ARM64).

## Install

See **[INSTALL.md](INSTALL.md)**. In short:

1. Run the hub on a server (one container).
2. On each computer:

   ```bash
   curl -fsSL https://github.com/Simplyph0/braid/releases/latest/download/install.sh | sh
   braid setup https://your-hub.example.com <token>
   braid add ~/Projects/my-project
   braid install
   ```

## Commands

| | |
|---|---|
| `braid status` | what each project is doing |
| `braid log <project>` | the project's moments, newest first |
| `braid jump <project> <moment>` | show an earlier moment here (your work is saved first) |
| `braid follow <project>` | back to the newest |
| `braid restore <project> <moment>` | make an earlier moment the newest, everywhere |
| `braid check <project> "<cmd>"` | a command (build, tests) a Claude merge must pass |
| `braid remove <project>` | stop syncing a folder (nothing is deleted) |

The web app (your hub's address in a browser) does the same from any device,
including your phone.

## How it works

A **moment** is the whole project folder at one point in time. Each computer
records a moment a few seconds after you stop changing files, then publishes
it to the project's **main** strand. If another computer published first,
Braid **braids** the two: fast-forward if only one side changed, otherwise
file-by-file and line-by-line, then Claude for overlapping edits (with no
tools: it only sees that file and can only answer with the merged text). A
braid that can't be made leaves that computer's work on its own strand.

Safety rules: a folder is recorded before anything changes it; a missing
folder (unplugged drive) pauses instead of counting as deleted; files being
edited and git's lock files are left alone; paths from the hub are checked
before anything is written.

## Building

```bash
./build.sh   # dist/braid and dist/braid-hub (needs Go 1.27)
go test ./...
scripts/demo.sh   # a hub and two pretend computers on this machine
```

Braid succeeds [ClaudeSync](https://github.com/Simplyph0/claudesync).
