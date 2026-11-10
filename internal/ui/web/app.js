"use strict";
// Braid's window on this PC. Talks to the braid program running here.
const app = document.getElementById("app");
let st = null, view = "home", openRow = null, typing = false;
let found = null, picked = new Set(), dismissed = false; // project folders found on this PC

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
  if (st.setUp && found === null) { // look once for project folders to suggest
    found = [];
    try { found = (await api("scan")).folders || []; picked = new Set(found.map(f => f.path)); } catch {}
  }
  render();
}

function render() {
  if (!st.setUp) return setup();
  if (view === "settings") return settings();
  home();
}

function setup(err) {
  typing = true;
  const hub = h("input", { type: "text", id: "hub", placeholder: "braid.example.com", autocomplete: "url", required: true });
  const pass = h("input", { type: "password", id: "pass", autocomplete: "new-password", required: true, minlength: "8" });
  const again = h("input", { type: "password", id: "again", autocomplete: "new-password", required: true });
  const msg = h("p", { class: "error", role: "alert" }, err || "");
  const go = h("button", { class: "btn primary", type: "submit" }, "Connect");
  show(
    h("header", {}, h("h1", {}, "Braid")),
    h("form", { class: "card", onsubmit: async e => {
      e.preventDefault();
      if (pass.value !== again.value) { msg.textContent = "The two passphrases don't match."; return; }
      go.disabled = true; go.textContent = "Connecting… (a few seconds)"; msg.textContent = "";
      try { await api("setup", { Hub: hub.value, Passphrase: pass.value }); typing = false; refresh(); }
      catch (e2) { msg.textContent = e2.message; go.disabled = false; go.textContent = "Connect"; }
    } },
      h("h2", { style: "margin:0 0 4px;font-size:18px" }, "Connect this PC"),
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
      h("button", { class: "btn", onclick: () => api("open", { What: "timeline" }).catch(e => alert(e.message)) }, "Timeline"),
      h("button", { class: "btn", onclick: () => { view = "settings"; render(); } }, "Settings")),
    suggestions(),
    ps.length ? h("div", { class: "list" }, ps.map((p, i) => item(p, states[i]))) :
      (found && found.length && !dismissed) ? null : h("div", { class: "list empty" }, h("h2", {}, "No folders yet"),
        h("p", {}, "Add a project folder and Braid keeps it the same on every PC you add it to. Add the same project on another PC and it fills in by itself."),
        h("button", { class: "btn primary", onclick: add }, "Add a folder")),
    ps.length ? h("div", { class: "add" }, h("button", { class: "btn primary", onclick: add }, "+ Add a folder")) : null,
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
      h("div", { class: "actions" },
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
    h("header", {}, h("button", { class: "btn", onclick: () => { view = "home"; render(); } }, "← Back"), h("h1", {}, "Settings")),
    h("div", { class: "list" },
      toggle("autostart", "Start Braid when I log in", "Keeps your folders in step in the background, with an icon by the clock."),
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
    h("footer", {}, `This PC: ${st.pc} · hub: ${st.hub} · braid ${st.version}`));
}

refresh();
setInterval(refresh, 3000);
