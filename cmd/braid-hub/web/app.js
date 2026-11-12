"use strict";
// Braid web app. Everything is decrypted here, in the browser, by braid.wasm.
const app = document.getElementById("app");
const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch {} },
  del(k) { try { localStorage.removeItem(k); } catch {} },
};
let data = [];
let current = null; // open project name
let open = new Set(); // expanded moments
let timer = null;
let latest = ""; // newest build the hub offers PCs
fetch("update/manifest.json", { cache: "no-cache" }).then(r => r.ok ? r.json() : null).then(m => { if (m) latest = m.version; }).catch(() => {});

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

// show replaces the page with kids, skipping empty ones.
function show(...kids) { app.replaceChildren(...kids.flat().filter(k => k != null && k !== false)); }

function toast(msg) {
  const t = document.getElementById("toast");
  t.textContent = msg; t.style.display = "block";
  clearTimeout(toast.t); toast.t = setTimeout(() => (t.style.display = "none"), 3500);
}

function ago(iso) {
  const s = (Date.now() - new Date(iso)) / 1000;
  if (!iso || iso.startsWith("0001")) return "never";
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + " min ago";
  if (s < 86400) return Math.floor(s / 3600) + " h ago";
  return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
const when = iso => new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

// A PC's state: dot colour and one line of words.
function pcState(pc, project) {
  // The hub says who is online (it hears each PC phone home every 30 s); an older hub doesn't, so go by the PC's own last report.
  const stale = pc.known ? !pc.online : (Date.now() - new Date(pc.updated)) / 1000 > 180;
  const seenAt = pc.known && pc.lastSeen && !pc.lastSeen.startsWith("0001") ? pc.lastSeen : pc.updated;
  if (stale) return { dot: "", text: "Offline · last seen " + ago(seenAt) };
  if (pc.error) return { dot: "bad", text: "Problem: " + pc.error };
  if (pc.conflicts && pc.conflicts.length) return { dot: "warn", text: "Its work waits: " + pc.conflicts.map(c => c.Path).join(", ") };
  if (pc.pin) return { dot: "warn", text: "Showing an older moment" };
  if (pc.at === project.main) return { dot: "ok", text: "Up to date" };
  return { dot: "ok", text: "Syncing…" };
}

async function boot() {
  show(h("p", { class: "empty" }, "Starting…"));
  const go = new Go();
  const res = await WebAssembly.instantiateStreaming(fetch("braid.wasm"), go.importObject);
  go.run(res.instance);
  const key = store.get("braid.key");
  if (key) {
    try { await braid.resume(location.origin, key); return home(); }
    catch (e) { toast("Please sign in again: " + e.message); }
  }
  login();
}

function login() {
  clearInterval(timer);
  const pass = h("input", { type: "password", id: "pass", autocomplete: "current-password", required: true });
  const remember = h("input", { type: "checkbox", id: "remember", checked: true });
  const btn = h("button", { class: "btn primary", type: "submit" }, "Open");
  const err = h("p", { class: "problem", role: "alert" });
  show(
    h("header", {}, h("h1", {}, "Braid")),
    h("form", { class: "card", onsubmit: async e => {
      e.preventDefault();
      btn.disabled = true; btn.textContent = "Unlocking… (a few seconds)"; err.textContent = "";
      try {
        const k = await braid.login(location.origin, pass.value);
        if (remember.checked) { store.set("braid.key", k); }
        else { store.del("braid.key"); }
        home();
      } catch (e2) { err.textContent = e2.message; btn.disabled = false; btn.textContent = "Open"; }
    } },
      h("label", { for: "pass" }, "Passphrase"),
      pass,
      h("p", { class: "muted" }, "The same one your PCs use. It stays in this browser."),
      h("label", { class: "check", for: "remember" }, remember, "Remember on this device"),
      btn, err));
}

let lastJSON = "";
async function refresh(force) {
  let j;
  try { j = await braid.overview(); }
  catch (e) { toast("Couldn't load: " + e.message); return; }
  // Redraw only when something changed, and never under an open menu.
  if (!force && (j === lastJSON || document.activeElement?.tagName === "SELECT")) return;
  lastJSON = j; data = JSON.parse(j);
  current ? project(current) : home(true);
}

async function home(fromRefresh) {
  current = null;
  if (!fromRefresh) {
    clearInterval(timer); timer = setInterval(refresh, 10000);
    show(h("p", { class: "empty" }, "Loading your projects…"));
    return refresh(true);
  }
  const settingsBtn = h("button", { class: "btn", onclick: hubSettingsView }, "Hub settings");
  const signOut = h("button", { class: "btn", onclick: () => { store.del("braid.key"); location.reload(); } }, "Sign out");
  show(
    h("header", {}, h("h1", {}, "Your projects"), settingsBtn, signOut),
    data.length ? null : h("p", { class: "empty" }, "No projects yet. On a PC: braid add <folder>"),
    ...data.map(p => {
      const last = p.moments[0];
      return h("button", { class: "card", onclick: () => { open.clear(); project(p.name); } },
        h("div", { class: "row" }, h("span", { class: "title" }, p.name),
          h("span", { class: "muted" }, last ? ago(last.time) : "")),
        last ? h("div", { class: "muted" }, last.pc + (last.intent ? " · " + last.intent : "")) : null,
        h("div", { class: "row", style: "margin-top:8px" }, ...p.pcs.map(pc => {
          const s = pcState(pc, p);
          return h("span", { class: "chip", title: s.text }, h("span", { class: "dot " + s.dot, "aria-hidden": "true" }), pc.pc, h("span", { class: "muted" }, "· " + s.text.split(":")[0]));
        })),
        p.waiting.length ? h("div", { class: "waiting" }, "Work waiting to be joined from: " + p.waiting.join(", ")) : null);
    }));
}

function project(name) {
  current = name;
  const p = data.find(x => x.name === name);
  if (!p) return home();
  const back = h("button", { class: "btn", onclick: () => home(true) }, "← Projects");
  show(
    h("header", {}, back, h("h1", {}, p.name)),
    h("h2", {}, "PCs"),
    p.pcs.length ? null : h("p", { class: "muted" }, "No PC has reported yet."),
    ...p.pcs.map(pc => pcCard(p, pc)),
    h("h2", {}, "Timeline"),
    ...p.moments.map(m => momentRow(p, m)));
}

function pcCard(p, pc) {
  const s = pcState(pc, p);
  const others = p.pcs.filter(o => o.key !== pc.key && o.at);
  const match = h("select", { class: "btn", "aria-label": "Make " + pc.pc + " match another PC", onchange: async e => {
    const other = others.find(o => o.key === e.target.value);
    if (!other) return;
    if (!confirm(`Make ${pc.pc} show exactly what ${other.pc} has?\n\n${pc.pc}'s current files are saved in the timeline first, so nothing is lost.`)) { e.target.value = ""; return; }
    await act(() => braid.pin(p.name, pc.key, other.at), `${pc.pc} will match ${other.pc} at its next sync.`);
  } }, h("option", { value: "" }, "Make it match…"), ...others.map(o => h("option", { value: o.key }, o.pc)));
  return h("div", { class: "card" },
    h("div", { class: "row" }, h("span", { class: "dot " + s.dot, "aria-hidden": "true" }), h("span", { class: "title" }, pc.pc),
      h("span", { class: "muted" }, "reported " + ago(pc.updated))),
    h("div", { class: "muted" }, pc.folder),
    h("div", { class: s.dot === "bad" ? "problem" : s.dot === "warn" ? "waiting" : "muted" }, s.text),
    pc.last ? h("div", { class: "muted" }, "Last: " + pc.last) : null,
    h("div", { class: "muted" }, "braid " + (pc.version || "?") + (latest && pc.version !== latest && pc.version !== "dev" ? ` · updating to ${latest} within the hour` : "")),
    ...(pc.conflicts || []).map(c => h("div", { class: "waiting" }, c.Path + ": " + c.Why)),
    h("div", { class: "actions" },
      pc.pin ? h("button", { class: "btn primary", onclick: () => act(() => braid.pin(p.name, pc.key, ""), `${pc.pc} goes back to the newest moment.`) }, "Follow the newest") : null,
      others.length ? match : null));
}

function momentRow(p, m) {
  const id = m.id;
  const isOpen = open.has(id);
  const body = h("div", {});
  const head = h("button", { class: "head", "aria-expanded": String(isOpen), onclick: () => { isOpen ? open.delete(id) : open.add(id); project(p.name); } },
    h("div", { class: "row" }, h("strong", {}, m.pc), h("span", { class: "muted" }, when(m.time)), m.braid ? h("span", { class: "chip" }, "braid") : null),
    h("div", {}, m.intent || h("span", { class: "muted" }, "Saved changes")));
  if (isOpen) {
    body.append(h("p", { class: "muted" }, "Loading changes…"));
    braid.changes(p.name, id).then(j => {
      const c = JSON.parse(j);
      const files = c.files || [];
      body.replaceChildren(
        h("ul", { class: "files" }, ...files.slice(0, 40).map(f => h("li", { class: f.Kind }, (f.Kind === "added" ? "+ " : f.Kind === "removed" ? "− " : "~ ") + f.Path)),
          files.length > 40 ? h("li", { class: "muted" }, `…and ${files.length - 40} more`) : null,
          c.git ? h("li", { class: "muted" }, `git: ${c.git} internal file(s)`) : null,
          !files.length && !c.git ? h("li", { class: "muted" }, "No file changes") : null),
        h("div", { class: "actions" },
          id !== p.main ? h("button", { class: "btn", onclick: async () => {
            if (!confirm("Make this moment the newest, on every PC?\n\nEverything after it stays in the timeline, so this can be undone.")) return;
            await act(() => braid.restore(p.name, id), "Restored. PCs following the newest switch at their next sync.");
          } }, "Restore as newest") : null,
          p.pcs.length ? h("select", { class: "btn", "aria-label": "Show this moment on a PC", onchange: async e => {
            const pc = p.pcs.find(o => o.key === e.target.value);
            if (!pc) return;
            await act(() => braid.pin(p.name, pc.key, id), `${pc.pc} will show this moment at its next sync. Its own files are saved first.`);
          } }, h("option", { value: "" }, "Show on a PC…"), ...p.pcs.map(o => h("option", { value: o.key }, o.pc))) : null));
    }).catch(e => body.replaceChildren(h("p", { class: "problem" }, e.message)));
  }
  return h("div", { class: "moment" + (m.braid ? " braid" : "") }, head, body);
}

async function act(fn, ok) {
  try { await fn(); toast(ok); await refresh(true); }
  catch (e) { toast("Didn't work: " + e.message); }
}

boot().catch(e => { show(h("p", { class: "problem" }, "Braid couldn't start in this browser: " + e.message)); });

// The hub's own settings: they apply at once, with no restart.
async function hubSettingsView() {
  clearInterval(timer);
  const backBtn = h("button", { class: "btn", onclick: () => home() }, "← Projects");
  show(h("header", {}, backBtn, h("h1", {}, "Hub settings")), h("p", { class: "muted" }, "Loading…"));
  let hs;
  try { hs = JSON.parse(await braid.hubSettings()); }
  catch (e) { return show(h("header", {}, backBtn, h("h1", {}, "Hub settings")), h("p", { class: "problem" }, e.message + " (an older hub can't change settings from here)")); }
  const opts = (list, cur) => list.map(([v, label]) => h("option", { value: v, selected: v === cur }, label));
  const offline = h("select", { class: "btn", id: "off" }, ...opts([[5, "5 seconds"], [30, "30 seconds"], [60, "1 minute"], [120, "2 minutes"], [300, "5 minutes"], [900, "15 minutes"]], hs.offlineSeconds));
  if (![5, 30, 60, 120, 300, 900].includes(hs.offlineSeconds)) offline.append(h("option", { value: hs.offlineSeconds, selected: true }, hs.offlineSeconds + " seconds"));
  const check = h("select", { class: "btn", id: "chk" }, ...opts([[1, "every minute"], [5, "every 5 minutes"], [15, "every 15 minutes"], [60, "every hour"], [1440, "once a day"]], hs.checkMinutes));
  if (![1, 5, 15, 60, 1440].includes(hs.checkMinutes)) check.append(h("option", { value: hs.checkMinutes, selected: true }, `every ${hs.checkMinutes} min`));
  const fetchBox = h("input", { type: "checkbox", id: "fetch", checked: hs.autoFetch });
  const msg = h("p", { class: "problem", role: "alert" });
  const save = h("button", { class: "btn primary", onclick: async () => {
    try { await braid.setHubSettings(+offline.value, +check.value, fetchBox.checked); toast("Saved. The hub uses it now."); msg.textContent = ""; }
    catch (e) { msg.textContent = e.message; }
  } }, "Save");
  show(h("header", {}, backBtn, h("h1", {}, "Hub settings")),
    h("div", { class: "card" },
      h("label", { for: "off", style: "display:block;font-weight:600;margin-bottom:4px" }, "Call a PC offline after"),
      h("p", { class: "muted", style: "margin:0 0 8px" }, "Every PC phones home every 30 seconds. If the hub hears nothing for this long, the PC shows as offline."), offline,
      h("label", { for: "chk", style: "display:block;font-weight:600;margin:18px 0 4px" }, "Look for new Braid versions"),
      h("p", { class: "muted", style: "margin:0 0 8px" }, "The hub fetches new signed versions from GitHub and hands them to your PCs."), check,
      h("label", { class: "toggle", for: "fetch", style: "padding:14px 0 0" }, fetchBox, h("div", {}, h("b", {}, "Fetch new versions automatically"), h("span", {}, "Turn off to keep the versions the hub has."))),
      h("div", { style: "margin-top:16px" }, save), msg));
}
