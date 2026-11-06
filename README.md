# BAS: Instagib – Community Server

Run your own game server for **BAS: Instagib** as a single Docker container. The server registers itself
with the official master server and shows up automatically in the in-game server browser
(<https://bas-instagib.com>). You do not need a domain, a certificate or an open TCP port – just **one UDP port**.

This repository contains no program code, only the Docker configuration for the official image
`ghcr.io/vrkttr/bas-instagib-server`. The official BAS: Instagib servers run exactly the same way.

---

## Contents

1. [Requirements](#requirements)
2. [Quick start (5 minutes)](#quick-start)
   - [Windows: one-click server (.exe)](#windows-one-click-server)
3. [Settings](#settings)
4. [Firewall and ports](#firewall-and-ports)
5. [Multiple servers on one machine](#multiple-servers-on-one-machine)
6. [Custom maps](#custom-maps)
7. [Updates](#updates)
8. [Reading logs and troubleshooting](#reading-logs-and-troubleshooting)
9. [Server ID and verification ✓](#server-id-and-verification)
10. [Why a modified image is not listed](#integrity-check)
11. [Resource usage](#resource-usage)
12. [Stopping and removing the server](#stopping-and-removing-the-server)
13. [Legal](#legal)

---

## Requirements

- A Linux server (VPS, dedicated server or a machine at home) with a **public IPv4 address**,
  or port forwarding on your router
- **Docker Engine 24+** with the Compose plugin (`docker compose version` must work)
- Outgoing internet access (HTTPS to the master server, UDP to detect the public IP)
- One free **UDP port** per server (default `40000`)

Install Docker (Debian/Ubuntu):

```bash
curl -fsSL https://get.docker.com | sh
```

## Quick start

```bash
git clone https://github.com/vrkttr/BAS-instagib-server.git bas-server
```
```bash
cd bas-server
```
```bash
cp .env.example .env
```
```bash
nano .env
```
Change at least `BAS_SERVER_NAME`, then start:
```bash
docker compose up -d
```
```bash
docker compose logs -f
```

After about 10 seconds the log shows:

```
[bas] Master server: registered as 3f9c2a7d1b0e4c55 (wss://masterserver.bas-instagib.com/gameserver)
[bas] Master server: UDP port reachable – server is listed in the server browser
```

From then on the server is listed in the game under **Play → Server browser**.

## Windows: one-click server

For Windows there is a ready-made launcher: **[BAS-Instagib-Server.exe](https://github.com/vrkttr/BAS-instagib-server/releases/latest/download/BAS-Instagib-Server.exe)**
(always the latest release, built automatically by GitHub Actions from the `windows/` folder).

1. Create an empty folder, e.g. `C:\BAS-Server`, and put the exe into it.
2. Double-click the exe. On the first start it asks for the settings and saves them in `config.env`
   next to the exe. On later starts you can press `E` to change them (or start the exe with `--setup`).
3. The launcher starts a **portable Docker engine**, pulls a **fresh** copy of the server image
   (the local image is deleted first, so nothing is reused from the cache) and starts the server.
   The window shows the server log.
4. **Closing the window stops the server** and the engine.

Everything lives in the folder of the exe:

| Path | Contents |
|---|---|
| `config.env` | your settings (same keys as `.env.example`) |
| `maps\` | custom maps (`.basc`), see [Custom maps](#custom-maps) |
| `engine\` | the portable Docker engine (virtual disk incl. the server ID) – do not delete, or the server gets a new ID |

Requirements and notes:

- Windows 10 2004+ or Windows 11 (x64) with **WSL2**. If WSL is missing, the launcher offers to run
  `wsl --install --no-distribution` (administrator prompt, then reboot). Docker Desktop is **not** needed.
- The engine is a private WSL2 distribution (Alpine Linux + Docker Engine) named `BAS-Instagib-xxxxxxxx`;
  it only runs while the window is open. First start downloads ~300 MB.
- WSL2 does not forward UDP from your network into Linux by default, so the launcher forwards the UDP port
  itself. **Allow the firewall prompt** for the exe (private and public networks) and forward the UDP port
  on your router as described in [Firewall and ports](#firewall-and-ports).
- If `networkingMode=mirrored` is set in `%USERPROFILE%\.wslconfig` (Windows 11), the server uses the
  Windows network directly and no forwarding is needed.
- To remove everything: close the launcher, run `wsl --unregister BAS-Instagib-xxxxxxxx`
  (name shown by `wsl -l`) and delete the folder.

## Settings

All settings live in the `.env` file. There are deliberately no others: the master server address, tick rate,
game rules and security features are built into the image and cannot be changed.

| Variable | Default | Meaning |
|---|---|---|
| `BAS_SERVER_NAME` | `BAS Community Server` | Name in the server browser (max. 48 characters) |
| `BAS_MODE` | `dm` | Game mode: `dm` (deathmatch) or `tdm` (team deathmatch) |
| `BAS_PORT` | `40000` | UDP port of the server (same port outside and inside) |
| `BAS_PASSWORD` | empty | Password; empty = public server |
| `BAS_MAPS` | `sulfur_gate` | Map pool, comma-separated (e.g. `sulfur_gate,mymap`); players vote after every match |
| `BAS_MAX_PLAYERS` | `12` | Player limit (1–12) |
| `BAS_PUBLIC_IP` | automatic | Only set this if automatic detection is wrong (see logs) |

Fixed values: 10-minute time limit, kill limit 25 (DM) / 50 (TDM), automatic team balance,
warmup until 2 players are present, queue for full servers.

After every change to `.env`:

```bash
docker compose up -d
```

## Firewall and ports

The only port you need to open is the UDP port from `BAS_PORT`. Example with `ufw`:

```bash
sudo ufw allow 40000/udp
```

- **Same port outside and inside:** The compose file maps `BAS_PORT` to the same port inside the container.
  If you use port forwarding on a router, forward the same port as well.
- **Cloud providers** (Hetzner, AWS, Oracle …): also open the port in the firewall/security group in their web console.
- No TCP port is needed. The server opens an outgoing connection to the master server itself.

## Multiple servers on one machine

The number of servers is only limited by CPU and bandwidth. Every server needs its **own UDP port**
and its **own data volume** (which holds its server ID).

### Option A: one folder per server (simplest)

```bash
git clone https://github.com/vrkttr/BAS-instagib-server.git server-dm
```
```bash
git clone https://github.com/vrkttr/BAS-instagib-server.git server-tdm
```

Create a separate `.env` in each folder with a **different** `BAS_PORT` (e.g. 40001, 40002) and run
`docker compose up -d` in each folder. Docker names containers and volumes after the folder, so the
servers do not interfere with each other.

### Option B: all servers in one file

`examples/multi-server.compose.yaml` starts four servers (DM and TDM, each public and password-protected)
on ports 40001–40004:

```bash
cp examples/multi-server.env.example examples/.env
```
```bash
nano examples/.env
```
```bash
docker compose -f examples/multi-server.compose.yaml up -d
```

To add more servers, copy a block under `services:`, change the name, the port (in both places) and the
volume, and add the volume under `volumes:` at the bottom.

## Custom maps

1. Build a map in the BAS map editor (<https://mapeditor.bas-instagib.com>), bake it and export it as `.basc`.
2. Put the file into the `maps/` folder, e.g. `maps/mymap.basc`.
3. Add it to `.env`: `BAS_MAPS=arena01,mymap`
4. `docker compose up -d`

Players download the map automatically through the master server when they join. The file name
(without `.basc`) is the map's identifier: letters, digits, `-` and `_` only.

## Updates

New versions are published as the image `ghcr.io/vrkttr/bas-instagib-server:latest`. **Servers must be up to
date**, otherwise the master server rejects them (see [Integrity check](#integrity-check)).

```bash
docker compose pull
```
```bash
docker compose up -d
```

Automatically every night at 4 am (crontab, `crontab -e`):

```
0 4 * * * cd /path/to/bas-server && docker compose pull -q && docker compose up -d
```

The server ID is kept across updates (volume `data`).

## Reading logs and troubleshooting

```bash
docker compose logs -f --tail=200
```

Every line starts with a timestamp (UTC). On startup the server prints its version, build ID, all settings,
the loaded maps, the detected public IP and the state of the master server connection. After that it logs
joins, disconnects, match phases (warmup, countdown, live, intermission), map changes and a status line
once per minute (when something changed).

| Log message | Cause | Fix |
|---|---|---|
| `UDP port reachable – server is listed in the server browser` | everything is fine | – |
| `UDP port NOT reachable` | firewall/port forwarding missing or wrong IP | open the UDP port (also at your cloud provider); same port outside and inside; set `BAS_PUBLIC_IP` if needed |
| `public IP could not be detected` | outgoing UDP is blocked | set `BAS_PUBLIC_IP=<your IPv4>` in `.env` |
| `REJECTED: Server code does not match the official build` | image is outdated or modified | `docker compose pull && docker compose up -d` |
| `Master server protocol … not supported` | image is outdated | update (see above) |
| `not reachable (wss://masterserver…)` | no internet/DNS on the host, or the master server is briefly offline | check the network; the server keeps retrying on its own |
| `Configuration error: …` | invalid value in `.env` | the message names the variable and the allowed values |
| `Map "xyz.basc" not found` | file missing in `maps/` or typo in `BAS_MAPS` | check the file name |
| `Too many servers from this address` | protection limit of the master server | contact the BAS: Instagib operator |
| `ID only valid until restart` | volume for `/app/data` is missing | use the compose file from this repository |

Container state (health check):

```bash
docker compose ps
```

When reporting a problem, please include the first ~40 lines after startup (`docker compose logs --tail=300`).
Passwords are never written to the log.

## Server ID and verification

On first start the server creates a key (in the `data` volume). The **server ID** (16 characters) is derived
from it and printed in the log. It stays the same as long as the volume exists.

Verified servers get a **✓** in the server browser. Verification is granted by the BAS: Instagib operator
based on the server ID. Ranked matches will later only count on verified servers.

## Integrity check

To make sure the same rules apply on every server, the master server **only accepts the unmodified official
image**. On every registration the server has to prove, using a one-time random value, that its program code
matches the official build byte for byte. A modified or outdated server is rejected and does not appear in
the server browser – the log then shows `REJECTED`.

Also built in and not configurable: the master server address, the game rules and the protection
mechanisms. The image runs without root privileges on a read-only file system.

## Resource usage

Per server with 12 players, roughly: 1 CPU core (128 Hz simulation, plan one core per server),
150 MB RAM, 1–2 Mbit/s upload. A small VPS with 2 cores easily handles two full servers.

## Stopping and removing the server

```bash
docker compose down
```

Also deleting the server ID (the server gets a new one on the next start):

```bash
docker compose down -v
```

## Legal

© 2026 David Vierkötter ("VRKTTR"). All rights reserved. The image may only be used unmodified to run a
BAS: Instagib server. See [LICENSE](LICENSE).

Official website: <https://bas-instagib.com>
