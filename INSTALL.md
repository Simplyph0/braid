# Installing Braid

You need:

- **A hub**: any always-on Linux machine your computers can reach (a home
  server, a Raspberry Pi, a small VPS).
- **Each computer**: Linux (any distribution) or Windows.

## 1. The hub

### With a container (recommended)

Works on any distribution with Podman or Docker, and keeps every computer
up to date: the image carries the latest signed builds.

#### Podman

```bash
podman run -d --name braid-hub --restart=always \
  -p 8490:8490 -v braid-data:/data \
  --label io.containers.autoupdate=registry \
  ghcr.io/simplyph0/braid-hub:latest
```

To update the hub (and through it, every computer) automatically with Podman,
run it as a systemd service and enable the update timer:

```bash
podman generate systemd --new --name braid-hub | sudo tee /etc/systemd/system/braid-hub.service
sudo systemctl daemon-reload && sudo systemctl enable --now braid-hub podman-auto-update.timer
```

#### Docker

```bash
docker run -d --name braid-hub --restart unless-stopped \
  -p 8490:8490 -v braid-data:/data \
  ghcr.io/simplyph0/braid-hub:latest
docker logs braid-hub   # shows the token
```

Or with Docker Compose, save this as `compose.yaml`:

```yaml
services:
  braid-hub:
    image: ghcr.io/simplyph0/braid-hub:latest
    restart: unless-stopped
    ports:
      - "8490:8490"
    volumes:
      - braid-data:/data
volumes:
  braid-data:
```

```bash
docker compose up -d
docker compose logs braid-hub   # shows the token
```

To update the hub (and through it, every computer):

```bash
docker compose pull && docker compose up -d
```

or, without Compose: `docker pull ghcr.io/simplyph0/braid-hub:latest`, then
`docker rm -f braid-hub` and the `docker run` line again (your data stays in
the `braid-data` volume). For automatic updates, add
[Watchtower](https://containrrr.dev/watchtower/).

### Or as a plain program

```bash
sudo curl -fsSL -o /usr/local/bin/braid-hub \
  https://github.com/Simplyph0/braid/releases/latest/download/braid-hub-linux-amd64   # or -arm64
sudo chmod +x /usr/local/bin/braid-hub
sudo mkdir -p /var/lib/braid
sudo braid-hub -data /var/lib/braid   # first run: note the token, then Ctrl-C
```

and run it as a service, e.g. `/etc/systemd/system/braid-hub.service`:

```ini
[Unit]
Description=Braid hub
After=network-online.target

[Service]
ExecStart=/usr/local/bin/braid-hub -data /var/lib/braid
Restart=always

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now braid-hub
```

A plain hub doesn't hand out updates; reinstall `braid` on your computers
for new versions.

### The token, and HTTPS

On its first start the hub prints a **token** (also saved in its data folder
as `token`) and the `braid setup` line for your computers:

```bash
podman logs braid-hub      # or: docker logs braid-hub, or: journalctl -u braid-hub
```

The hub speaks plain HTTP on port 8490. Your files are encrypted before they
leave each computer, but the token isn't, so outside your home network put
the hub behind HTTPS: a reverse proxy (Caddy, nginx, Traefik) or a tunnel
(Cloudflare Tunnel, Tailscale).

## 2. Each computer

### Linux (any distribution)

```bash
curl -fsSL https://github.com/Simplyph0/braid/releases/latest/download/install.sh | sh
```

This puts one static program in `~/.local/bin/braid` (no dependencies; works
on old and new distributions alike). Without curl: `wget -qO- <same URL> | sh`.

### Windows

In PowerShell:

```powershell
$d = "$env:LOCALAPPDATA\Programs\Braid"; New-Item -ItemType Directory -Force $d | Out-Null
Invoke-WebRequest https://github.com/Simplyph0/braid/releases/latest/download/braid-windows-amd64.exe -OutFile "$d\braid.exe"
[Environment]::SetEnvironmentVariable("Path", $env:Path + ";$d", "User")
```

(On ARM: `braid-windows-arm64.exe`.) Open a new terminal afterwards.

### Connect it

```bash
braid setup https://your-hub.example.com <token>
```

It asks for a **passphrase**. Use the same one on every computer: it
encrypts everything, and the hub never sees it. If you lose it, your data
can't be recovered.

```bash
braid add ~/Projects/my-project   # repeat for each project
braid install                     # start Braid when you log in
braid status
```

`braid install` uses a systemd user service where there is one, your
desktop's startup apps otherwise, and the sign-in list on Windows.
`braid uninstall` undoes it.

A folder that already exists on another computer fills in at the next sync:
add an empty (or missing) folder with the same project name.

### Optional: Claude merges

If [Claude Code](https://claude.com/claude-code) is installed and signed in
on a computer, Braid asks it to join edits that overlap. To turn that off on
a computer, set `"noClaude": true` in its config file:

- Linux: `~/.config/braid/config.json`
- Windows: `%AppData%\braid\config.json`

`"noUpdate": true` there turns off automatic updates.

## 3. The web app

Open your hub's address in a browser and sign in with the token and your
passphrase. It shows every project, each computer's state and the timeline,
and can restore moments or point a computer at any moment.

## Removing Braid

On each computer: `braid uninstall`, then delete the program and its config
folder. Your project folders stay as they are. On the hub: remove the
container (or service) and its data volume.
