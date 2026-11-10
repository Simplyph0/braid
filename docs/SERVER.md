# Running the hub on your server (Cockpit / Podman)

The hub is published as a container image, `ghcr.io/simplyph0/braid-hub`,
for every release. It holds the signed `braid` builds too, so updating the
hub is also how every PC gets updated: each PC installs a newer signed build
from the hub within an hour.

The repository is private, so the server signs in to GitHub's registry once:

```bash
podman login ghcr.io -u Simplyph0
```

(The password is a GitHub token with the `read:packages` permission:
GitHub → Settings → Developer settings → Personal access tokens.)

Then save this as `/etc/containers/systemd/braid-hub.container`:

```ini
[Container]
Image=ghcr.io/simplyph0/braid-hub:latest
PublishPort=8490:8490
Volume=braid-data:/data
AutoUpdate=registry

[Service]
Restart=always

[Install]
WantedBy=multi-user.target default.target
```

and start it, with automatic updates every day:

```bash
sudo systemctl daemon-reload
sudo systemctl start braid-hub
sudo systemctl enable --now podman-auto-update.timer
```

The hub's first log lines (`journalctl -u braid-hub`, or Cockpit → Services →
braid-hub) show the token and the `braid setup` line for your PCs. The web
app is on port 8490; put it behind your Cloudflare tunnel like the old
ClaudeSync server.

Older Podman (before 4.4) doesn't read `.container` files: run the image with
`podman run -d --name braid-hub -p 8490:8490 -v braid-data:/data --label
io.containers.autoupdate=registry ghcr.io/simplyph0/braid-hub:latest`, then
`podman generate systemd --new --name braid-hub` to make it a service.
