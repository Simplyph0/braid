# Milestone 0: what we checked before building

**Claude desktop app and chat files.** ClaudeSync's experience (v0.5–0.7.2)
shows the desktop app keeps its Code-tab session list in memory: records
changed under it while it runs are ignored or overwritten. So Braid applies
desktop-app session records only while the app is closed, and never closes it
itself. Claude Code transcripts (`~/.claude/projects/*/*.jsonl`) are only read
when a session is resumed, so they can be written any time a session isn't
open on that PC.

**Claude merges, headless.** Claude Code 2.1.312 has everything the merger
needs: `claude -p` (no UI), `--output-format json` with `--json-schema` (the
merged files come back as structured output), `--no-session-persistence` (a
merge doesn't show up as a chat), `--allowedTools` / `--disallowedTools` (a
merge gets no tools: it can't run commands or touch files), and `--model`.
It runs on the PC under the user's own Claude sign-in.
