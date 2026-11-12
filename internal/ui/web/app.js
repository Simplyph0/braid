"use strict";
// Braid's window on this PC. Talks to the braid program running here.
const app = document.getElementById("app");
let st = null, view = "home", openRow = null, typing = false;
let found = null, picked = new Set(), dismissed = false; // project folders found on this PC
let chats = null; // what the last chat sync did

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid instanceof Node ? kid : String(kid));
  return el;
}
function show(...kids) { app.replaceChildren(...kids.flat().filter(k => k != null && k !== false)); }

async function api(path, body) {
  const r = await fetch("/api/" + path, body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json", "X-Braid": "1" }, body: JSON.stringify(body),
  });
  const j = await r.json().catch(() => ({ error: "Braid isn't answering. Is it still running?" }));
  if (!r.ok || j.error) throw new Error(j.error || r.statusText);
  return j;
}

function ago(iso) {
  if (!iso || iso.startsWith("0001")) return "";
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 10) return "just now";
  if (s < 60) return Math.floor(s) + " s ago";
  if (s < 3600) return Math.floor(s / 60) + " min ago";
  return Math.floor(s / 3600) + " h ago";
}

// One project's state, in a colour and a few words.
function stateOf(p) {
  const s = p.status || {};
  if (p.paused) return { dot: "", cls: "", text: "Paused" };
  if (s.error) return { dot: "bad", cls: "bad", text: s.error };
  if (s.conflicts && s.conflicts.length) return { dot: "warn", cls: "warn", text: `${s.conflicts.length} file(s) changed in the same place on two PCs: waiting to be joined` };
  if (!s.lastSync || s.lastSync.startsWith("0001")) return { dot: "busy", cls: "", text: "Starting…" };
  if (s.detached) return { dot: "warn", cls: "warn", text: "Showing an older moment (open the timeline to come back)" };
  return { dot: "ok", cls: "", text: "Up to date · checked " + ago(s.lastSync) };
}

async function refresh() {
  if (typing) return;
  try { st = await api("state"); } catch (e) { show(h("p", { class: "error" }, e.message)); return; }
  if (st.setUp) { try { chats = await api("chats"); } catch { chats = null; } }
  if (st.setUp && found === null) { // look once for project folders to suggest
    found = [];
    try { found = (await api("scan")).folders || []; picked = new Set(found.map(f => f.path)); } catch {}
  }
  if (st.setUp && view !== "home" && view !== "settings") return; // those screens load once, not every few seconds
  render();
}

function render() {
  if (!st.setUp || view === "setup") return setup();
  if (view === "settings") return settings();
  if (view === "history") return history();
  if (view === "hub") return hubView();
  if (view === "log") return logView();
  home();
}

const goto = v => { view = v; render(); };
const back = (to, label) => h("button", { class: "btn", onclick: () => goto(to) }, label || "← Back");

function setup(err) {
  typing = true;
  const changing = st.setUp; // connecting to a different hub, or re-entering the passphrase
  const hub = h("input", { type: "text", id: "hub", placeholder: "braid.example.com", autocomplete: "url", required: true, value: changing ? st.hub : "" });
  const pass = h("input", { type: "password", id: "pass", autocomplete: "new-password", required: true, minlength: "8" });
  const again = h("input", { type: "password", id: "again", autocomplete: "new-password", required: true });
  const msg = h("p", { class: "error", role: "alert" }, err || "");
  const go = h("button", { class: "btn primary", type: "submit" }, "Connect");
  show(
    h("header", {}, changing ? back("settings") : null, h("h1", {}, changing ? "Change hub" : "Braid")),
    h("form", { class: "card", onsubmit: async e => {
      e.preventDefault();
      if (pass.value !== again.value) { msg.textContent = "The two passphrases don't match."; return; }
      go.disabled = true; go.textContent = "Connecting… (a few seconds)"; msg.textContent = "";
      try { await api("setup", { Hub: hub.value, Passphrase: pass.value }); typing = false; view = "home"; found = null; refresh(); }
      catch (e2) { msg.textContent = e2.message; go.disabled = false; go.textContent = "Connect"; }
    } },
      h("h2", { style: "margin:0 0 4px;font-size:18px" }, changing ? "Connect to a different hub" : "Connect this PC"),
      h("p", { class: "hint" }, "Braid keeps your projects the same on all your PCs, through your own hub."),
      h("label", { class: "field", for: "hub" }, "Hub address"), hub,
      h("label", { class: "field", for: "pass" }, "Passphrase"), pass,
      h("p", { class: "hint" }, "The same on every PC. On your first PC, you're choosing it now. It encrypts everything and can't be recovered, so keep it somewhere safe."),
      h("label", { class: "field", for: "again" }, "Passphrase again"), again,
      h("div", { style: "margin-top:16px" }, go), msg));
  hub.focus();
}

function home() {
  typing = false;
  const ps = st.projects || [];
  const states = ps.map(stateOf);
  const bad = states.filter(s => s.dot === "bad").length, warn = states.filter(s => s.dot === "warn").length;
  const summary = !ps.length ? "" : bad ? `${bad} need${bad > 1 ? "" : "s"} a look` : warn ? `${warn} waiting` : "Everything is up to date";
  const sumDot = !ps.length ? null : bad ? "bad" : warn ? "warn" : "ok";
  const addMsg = h("p", { class: "error", role: "alert" });
  const pathInput = h("input", { type: "text", "aria-label": "Folder path", placeholder: "/path/to/folder" });
  const typed = h("form", { class: "add", style: "display:none", onsubmit: async e => {
    e.preventDefault();
    try { await api("add", { Folder: pathInput.value }); refresh(); } catch (e2) { addMsg.textContent = e2.message; }
  } }, h("div", { class: "grow" }, pathInput), h("button", { class: "btn", type: "submit" }, "Add"));
  pathInput.addEventListener("focus", () => (typing = true));
  pathInput.addEventListener("blur", () => (typing = false));
  const add = async () => {
    addMsg.textContent = "";
    try {
      const r = await api("pick", {});
      if (r.folder) { await api("add", { Folder: r.folder }); refresh(); }
    } catch (e) { addMsg.textContent = e.message; typed.style.display = "flex"; pathInput.focus(); }
  };
  show(
    h("header", {}, h("h1", {}, "Braid"),
      h("div", { class: "summary" }, sumDot ? h("span", { class: "dot " + sumDot, "aria-hidden": "true" }) : null, summary),
      h("button", { class: "btn", onclick: () => api("open", { What: "timeline" }).catch(e => alert(e.message)) }, "Hub"),
      h("button", { class: "btn", onclick: () => goto("settings") }, "Settings")),
    chatsNotice(),
    suggestions(),
    ps.length ? h("div", { class: "list" }, ps.map((p, i) => item(p, states[i]))) :
      (found && found.length && !dismissed) ? null : h("div", { class: "list empty" }, h("h2", {}, "No folders yet"),
        h("p", {}, "Add a project folder and Braid keeps it the same on every PC you add it to. Add the same project on another PC and it fills in by itself."),
        h("button", { class: "btn primary", onclick: add }, "Add a folder")),
    h("div", { class: "add" },
      ps.length ? h("button", { class: "btn primary", onclick: add }, "+ Add a folder") : null,
      h("button", { class: "btn", onclick: () => goto("hub") }, "Restore a project from your hub…")),
    typed, addMsg,
    h("footer", {}, `${st.pc} · connected to ${st.hub.replace(/^https?:\/\//, "")} · braid ${st.version}`));
}

// Folders found on this PC that look like projects: ticked, one click to add.
function suggestions() {
  if (dismissed || !found || !found.length) return null;
  const msg = h("p", { class: "error", role: "alert" });
  const btn = h("button", { class: "btn primary" }, "");
  const label = () => { btn.textContent = picked.size ? `Add ${picked.size} folder${picked.size > 1 ? "s" : ""}` : "Nothing selected"; btn.disabled = !picked.size; };
  label();
  btn.addEventListener("click", async () => {
    btn.disabled = true; btn.textContent = "Adding…";
    try { await api("addmany", { Folders: [...picked] }); found = null; dismissed = false; refresh(); }
    catch (e) { msg.textContent = e.message; label(); }
  });
  return h("div", { class: "card", style: "margin-bottom:14px" },
    h("div", { style: "font-weight:600" }, `Found ${found.length} folder${found.length > 1 ? "s" : ""} you've used with Claude Code`),
    h("p", { class: "hint", style: "margin-bottom:10px" }, "Braid will keep the ticked ones the same on all your PCs. Untick any you don't want."),
    h("div", { class: "found" }, found.map(f => {
      const box = h("input", { type: "checkbox", id: "f" + f.path, checked: picked.has(f.path) });
      box.addEventListener("change", () => { box.checked ? picked.add(f.path) : picked.delete(f.path); label(); });
      return h("label", { class: "toggle", for: "f" + f.path, style: "padding:8px 4px" }, box,
        h("div", {}, h("b", {}, f.name, f.git ? h("span", { class: "tag" }, "git") : null), h("span", {}, f.path)));
    })),
    h("div", { class: "actions", style: "margin-top:12px" }, btn,
      h("button", { class: "btn", onclick: () => { dismissed = true; render(); } }, "Not now")),
    msg);
}

// The command a Claude merge must pass in this project (e.g. a build).
function checkRow(p) {
  const input = h("input", { type: "text", value: p.check || "", placeholder: "e.g. go build ./...  (empty: no check)", "aria-label": "Merge check command" });
  input.addEventListener("focus", () => (typing = true));
  input.addEventListener("blur", () => (typing = false));
  const save = h("button", { class: "btn", onclick: async () => {
    try { await api("check", { Name: p.name, Check: input.value }); typing = false; refresh(); save.textContent = "Saved"; } catch (e) { alert(e.message); }
  } }, "Save");
  return h("div", {},
    h("div", { class: "muted", style: "margin-bottom:4px" }, "When Claude joins edits in this project, they must pass this command, or they wait instead."),
    h("div", { class: "add", style: "margin:0" }, h("div", { class: "grow" }, input), save));
}

// Chats from your other PCs that are waiting for the Claude desktop app to close.
function closeClaudeButton(label) {
  const b = h("button", { class: "btn primary", onclick: async () => {
    if (!confirm("Close the Claude desktop app, load the waiting chats, and open it again?\n\nAnything Claude is doing right now stops.")) return;
    b.disabled = true; b.textContent = "Closing Claude…";
    try { await api("closeclaude", {}); alert("Done. The chats are loaded and Claude is open again."); } catch (e) { alert(e.message); }
    chats = null; refresh();
  } }, label || "Close Claude and load them");
  return b;
}
function chatsNotice() {
  const s = chats && chats.enabled && chats.status;
  if (!s || !s.hasApp || !s.pending) return null;
  return h("div", { class: "card", style: "margin-bottom:14px" },
    h("div", { style: "font-weight:600" }, `${s.pending} chat${s.pending > 1 ? "s" : ""} from your other PCs ${s.pending > 1 ? "are" : "is"} waiting for the Claude app to close`),
    h("p", { class: "hint", style: "margin:4px 0 10px" }, s.appRunning ? "Claude only reads its chat list when it starts. Close it yourself, or let Braid do it." : "Claude is closed now: they load within a minute."),
    s.appRunning ? closeClaudeButton() : null);
}

function item(p, s) {
  const isOpen = openRow === p.name;
  const act = (body, confirmText) => async () => {
    if (confirmText && !confirm(confirmText)) return;
    try { await api("project", body); refresh(); } catch (e) { alert(e.message); }
  };
  return h("div", { class: "item" },
    h("div", { class: "rowwrap" },
      h("button", { class: "row", "aria-expanded": String(isOpen), onclick: () => { openRow = isOpen ? null : p.name; render(); } },
        h("span", { class: "dot " + s.dot, "aria-hidden": "true" }),
        h("div", { class: "grow" },
          h("div", { class: "name" }, p.name),
          h("div", { class: "state " + s.cls }, s.text))),
      h("button", { class: "btn", onclick: () => api("open", { Name: p.name }).catch(er => alert(er.message)) }, "Open folder")),
    isOpen ? h("div", { class: "details" },
      h("div", { class: "muted" }, p.folder),
      p.status && p.status.last ? h("div", { class: "muted" }, "Last: " + p.status.last) : null,
      ...((p.status && p.status.conflicts) || []).map(c => h("div", { class: "muted" }, "• " + c.Path + ": " + c.Why)),
      checkRow(p),
      h("div", { class: "actions" },
        h("button", { class: "btn primary", onclick: () => { histName = p.name; histOpen.clear(); hist = null; goto("history"); } }, "History & restore"),
        p.paused ? h("button", { class: "btn", onclick: act({ Name: p.name, Do: "resume" }) }, "Resume")
                 : h("button", { class: "btn", onclick: act({ Name: p.name, Do: "pause" }) }, "Pause"),
        h("button", { class: "btn danger", onclick: act({ Name: p.name, Do: "remove" },
          `Stop syncing ${p.name} on this PC?\n\nThe folder and its files stay as they are, and so does its timeline on your hub and on your other PCs.`) }, "Stop syncing here"))) : null);
}

function settings() {
  typing = false;
  const toggle = (key, title, desc, invert) => {
    const box = h("input", { type: "checkbox", id: key, checked: invert ? !st[key] : !!st[key] });
    box.addEventListener("change", async () => {
      try { await api("settings", { [key[0].toUpperCase() + key.slice(1)]: invert ? !box.checked : box.checked }); refresh(); }
      catch (e) { alert(e.message); box.checked = !box.checked; }
    });
    return h("label", { class: "toggle", for: key }, box, h("div", {}, h("b", {}, title), h("span", {}, desc)));
  };
  show(
    h("header", {}, back("home"), h("h1", {}, "Settings")),
    h("div", { class: "list" },
      h("div", { class: "toggle" }, h("div", { style: "flex:1" }, h("b", {}, "This PC's name"), h("span", {}, "How this PC is shown on your other PCs and in the web app."),
        (() => {
          const n = h("input", { type: "text", value: st.pc, "aria-label": "This PC's name", style: "margin-top:8px;display:block" });
          n.addEventListener("focus", () => (typing = true));
          n.addEventListener("blur", () => (typing = false));
          n.addEventListener("change", async () => { try { await api("settings", { PCName: n.value }); typing = false; refresh(); } catch (e) { alert(e.message); } });
          return n;
        })())),
      toggle("autostart", "Start Braid when I log in", "Keeps your folders in step in the background, with an icon by the clock."),
      toggle("noChats", "Back up all my Claude chats", "Every Claude Code chat on this PC, and the Claude desktop app's own chat list, is backed up to your hub and brought to your other PCs. Chats are only ever added, never deleted.", true),
      toggle("noClaude", "Let Claude join overlapping edits", "When two PCs changed the same lines, Claude Code on this PC merges them. It only sees that file, with no tools. Needs Claude Code signed in.", true),
      toggle("noUpdate", "Install updates automatically", "New versions come from your hub, signed by the Braid release key.", true),
      h("label", { class: "toggle", for: "every" },
        h("div", { style: "flex:1" }, h("b", {}, "Look for updates"), h("span", {}, "How often this PC asks your hub for a new version.")),
        (() => {
          const sel = h("select", { id: "every", class: "btn" },
            ...[[1, "every minute"], [5, "every 5 minutes"], [15, "every 15 minutes"], [60, "every hour"], [1440, "once a day"]]
              .map(([m, label]) => h("option", { value: m, selected: m === st.updateMinutes }, label)));
          if (![1, 5, 15, 60, 1440].includes(st.updateMinutes)) sel.append(h("option", { value: st.updateMinutes, selected: true }, `every ${st.updateMinutes} min`));
          sel.addEventListener("change", async () => { try { await api("settings", { UpdateMinutes: +sel.value }); refresh(); } catch (e) { alert(e.message); } });
          return sel;
        })())),
    chatsCard(),
    h("div", { class: "add" },
      h("button", { class: "btn", onclick: () => goto("setup") }, "Change hub or passphrase…"),
      h("button", { class: "btn", onclick: () => goto("log") }, "Activity log")),
    h("footer", {}, `This PC: ${st.pc} · hub: ${st.hub} · braid ${st.version}`));
}

function chatsCard() {
  const s = chats && chats.status;
  if (!chats || !chats.enabled) return h("p", { class: "hint", style: "margin-top:14px" }, "Chat backup is off.");
  const lines = [];
  if (!s || !s.updated || s.updated.startsWith("0001")) lines.push("Waiting for the first chat sync…");
  else {
    lines.push(`${s.files} chat file${s.files === 1 ? "" : "s"} on this PC · last synced ${ago(s.updated)}`);
    if (s.pushed || s.pulled) lines.push(`Last time: ${s.pushed} sent to your hub, ${s.pulled} brought here`);
    if (!s.hasApp) lines.push("The Claude desktop app isn't on this PC, so only Claude Code chats are synced.");
  }
  const sync = h("button", { class: "btn", onclick: async () => {
    sync.disabled = true; sync.textContent = "Syncing…";
    try { await api("chats/now", {}); } catch (e) { alert(e.message); }
    chats = null; refresh();
  } }, "Sync chats now");
  return h("div", { class: "card", style: "margin-top:14px" },
    h("div", { style: "font-weight:600;margin-bottom:4px" }, "Claude chats"),
    ...lines.map(l => h("div", { class: "muted" }, l)),
    s && s.error ? h("div", { class: "error" }, s.error) : null,
    h("div", { class: "actions", style: "margin-top:10px" }, sync, s && s.hasApp && s.pending ? closeClaudeButton() : null));
}

// ---- restore a project from the hub ----
let hubList = null;
async function hubView() {
  typing = false;
  show(h("header", {}, back("home"), h("h1", {}, "Restore from your hub")), h("p", { class: "hint" }, "Loading…"));
  try { hubList = (await api("hub")).projects; } catch (e) { return show(h("header", {}, back("home"), h("h1", {}, "Restore from your hub")), h("p", { class: "error" }, e.message)); }
  const msg = h("p", { class: "error", role: "alert" });
  const rows = hubList.map(p => {
    const folder = h("input", { type: "text", value: `${st.home}/Projects/${p.name}`, "aria-label": "Folder for " + p.name });
    folder.addEventListener("focus", () => (typing = true));
    folder.addEventListener("blur", () => (typing = false));
    const go = h("button", { class: "btn primary", onclick: async () => {
      msg.textContent = "";
      if (!confirm(`Put ${p.name} in\n${folder.value}\n\nIf the folder exists and has files, Braid joins them with the project; nothing is deleted without being saved in the timeline.`)) return;
      go.disabled = true; go.textContent = "Restoring…";
      try { await api("restoreproject", { Name: p.name, Folder: folder.value }); typing = false; view = "home"; refresh(); }
      catch (e) { msg.textContent = e.message; go.disabled = false; go.textContent = "Restore"; }
    } }, "Restore");
    const pick = h("button", { class: "btn", onclick: async () => { try { const r = await api("pick", {}); if (r.folder) folder.value = r.folder; } catch (e) { msg.textContent = e.message; } } }, "Choose…");
    return h("div", { class: "item" }, h("div", { class: "details", style: "padding:14px" },
      h("div", {}, h("b", {}, p.name), h("span", { class: "muted" }, p.pc ? `  ·  last changed on ${p.pc}` : "")),
      h("div", { class: "add", style: "margin:0" }, h("div", { class: "grow" }, folder), pick, go)));
  });
  show(h("header", {}, back("home"), h("h1", {}, "Restore from your hub")),
    h("p", { class: "hint", style: "margin-bottom:12px" }, "Projects on your hub that this PC doesn't have yet. Pick where each goes."),
    rows.length ? h("div", { class: "list" }, rows) : h("div", { class: "list empty" }, h("h2", {}, "Nothing to restore"), h("p", {}, "Every project on your hub is already on this PC.")),
    msg);
}

// ---- a project's history ----
let histName = null, hist = null, histOpen = new Set();
async function history() {
  typing = false;
  const title = h("h1", {}, histName + " · History");
  if (!hist) {
    show(h("header", {}, back("home"), title), h("p", { class: "hint" }, "Loading…"));
    try { hist = await api("timeline?name=" + encodeURIComponent(histName)); } catch (e) { return show(h("header", {}, back("home"), title), h("p", { class: "error" }, e.message)); }
  }
  const act = (body, ok) => async () => {
    try { await api("moment", body); hist = null; alert(ok); history(); } catch (e) { alert(e.message); }
  };
  const rows = hist.moments.map(m => {
    const open = histOpen.has(m.id), here = m.id === hist.here, newest = m.id === hist.main;
    const body = h("div", {});
    if (open) {
      body.append(h("p", { class: "hint" }, "Loading changes…"));
      api(`changes?name=${encodeURIComponent(histName)}&id=${m.id}`).then(c => {
        const files = c.files || [];
        body.replaceChildren(
          h("ul", { class: "files" }, files.slice(0, 40).map(f => h("li", {}, (f.kind === "added" ? "+ " : f.kind === "removed" ? "− " : "~ ") + f.path)),
            files.length > 40 ? h("li", { class: "muted" }, `…and ${files.length - 40} more`) : null,
            c.git ? h("li", { class: "muted" }, `git: ${c.git} internal file(s)`) : null,
            !files.length && !c.git ? h("li", { class: "muted" }, "No file changes") : null),
          h("div", { class: "actions", style: "margin-top:8px" },
            !here ? h("button", { class: "btn", onclick: act({ Name: histName, ID: m.id, Do: "show" }, "This PC will show that moment in a few seconds. Whatever is here now is saved first.") }, "Show on this PC") : null,
            !newest ? h("button", { class: "btn", onclick: () => confirm("Make this moment the newest, on every PC?\n\nEverything after it stays in the timeline, so this can be undone.") && act({ Name: histName, ID: m.id, Do: "restore" }, "Restored. Every PC following the newest switches to it.")() }, "Restore as newest on all PCs") : null));
      }).catch(e => body.replaceChildren(h("p", { class: "error" }, e.message)));
    }
    return h("div", { class: "item" },
      h("button", { class: "row", "aria-expanded": String(open), onclick: () => { open ? histOpen.delete(m.id) : histOpen.add(m.id); history(); } },
        h("span", { class: "dot " + (here ? "ok" : ""), "aria-hidden": "true" }),
        h("div", { class: "grow" },
          h("div", { class: "name" }, m.pc, h("span", { class: "muted", style: "font-weight:400" }, "  " + new Date(m.time).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })),
            m.braid ? h("span", { class: "tag" }, "braid") : null, here ? h("span", { class: "tag" }, "shown on this PC") : null, newest ? h("span", { class: "tag" }, "newest") : null),
          h("div", { class: "sub" }, m.intent || "Saved changes"))),
      open ? h("div", { class: "details" }, body) : null);
  });
  show(h("header", {}, back("home"), title),
    hist.pinned ? h("div", { class: "card", style: "margin-bottom:12px" }, h("b", {}, "This PC is showing an older moment."), h("div", { class: "actions", style: "margin-top:8px" },
      h("button", { class: "btn primary", onclick: act({ Name: histName, Do: "follow" }, "This PC goes back to the newest moment in a few seconds.") }, "Follow the newest"))) : null,
    h("div", { class: "list" }, rows));
}

// ---- activity log ----
async function logView() {
  typing = false;
  show(h("header", {}, back("settings"), h("h1", {}, "Activity log")), h("p", { class: "hint" }, "Loading…"));
  let lines;
  try { lines = (await api("log")).lines; } catch (e) { return show(h("header", {}, back("settings"), h("h1", {}, "Activity log")), h("p", { class: "error" }, e.message)); }
  show(h("header", {}, back("settings"), h("h1", {}, "Activity log")),
    h("pre", { class: "log" }, lines.length ? lines.join("\n") : "Nothing yet."));
}

refresh();
setInterval(refresh, 3000);
