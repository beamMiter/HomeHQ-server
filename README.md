# HomeHQ Server

The API behind HomeHQ, one place to run a household: bills, purchases, notes,
chores, medicine stock, contractors (with a Google Places lookup) and home
cameras (Imou). Built with Go.

The Flutter app lives in its own repository:
[HomeHQ-client](https://github.com/beamMiter/HomeHQ-client).

---

## Tech Stack

- **Language:** Go 1.25+
- **Router:** Chi
- **Database:** PostgreSQL 16
- **Auth:** JWT (HS256)
- **Storage:** Local / Cloudflare R2
- **Migration:** Goose
- **Container:** Docker + Docker Compose

---

## Project Structure

```
HomeHQ-server/
├── cmd/
│   ├── api/          # Main server entrypoint
│   ├── migrate/      # Migration runner
│   └── seed/         # Database seeder
├── internal/
│   ├── appliances/   # Appliance control module (lights, plugs, aircon...)
│   ├── auth/         # JWT auth middleware
│   ├── bills/        # Bills module
│   ├── cameras/      # Home cameras (Imou / fake driver)
│   ├── chores/       # Chores module
│   ├── config/       # App configuration
│   ├── contractors/  # Contractors module
│   ├── db/           # Database connection
│   ├── files/        # File upload module
│   ├── health/       # Health check handlers
│   ├── httpx/        # HTTP helpers & middleware
│   ├── medicine/     # Medicine stock module
│   ├── notes/        # Notes module
│   ├── places/       # Google Places lookup and starred places
│   ├── purchases/    # Purchases module
│   ├── storage/      # Storage adapters (Local/R2)
│   └── user/         # User & auth module
├── migrations/       # SQL migration files
├── .env              # Docker Compose variables (yours, not committed)
├── docker-compose.yml
├── Dockerfile
└── Makefile
```

---

## Getting Started

### Prerequisites

- Go 1.25+
- PostgreSQL 16 (or Docker)
- Make (optional)

### 1. Clone & Setup

```bash
git clone https://github.com/beamMiter/HomeHQ-server.git
cd HomeHQ-server
```

The API reads the variables of the shell it runs in, and every variable has a
development default (see [Environment Variables](#environment-variables)), so a
plain local run needs no file. Only Docker Compose reads a `.env` next to
`docker-compose.yml`; create it yourself with `POSTGRES_DB`, `POSTGRES_USER`
and `POSTGRES_PASSWORD`, plus any variable from the table you want to override.
It is git-ignored.

### 2. Start Database

```bash
docker compose up -d db
```

### 3. Run Migrations

```bash
go run ./cmd/migrate -cmd=up
```

### 4. Seed Database

```bash
go run ./cmd/seed
```

Dev accounts (local only — their passwords are written in the source, so the
seed refuses `APP_ENV=production` and refuses any database that is not on this
machine unless you pass `-allow-remote`):

| Role | Email | Password |
|---|---|---|
| admin | `admin@example.com` | `admin2026` |
| member | `user@example.com` | `user2026` |

### Real accounts (sign-up is closed)

Every account can read all of the household's data, so the API has no sign-up
(`POST /auth/register` does not exist; the route is commented out in
`cmd/api/main.go` and the handler is kept for later). The owner makes accounts
from the command line, which talks to the database directly. The password goes
in through a variable, never a flag, so it stays out of shell history:

```bash
# Type it twice: the password is not echoed, so a typo is invisible and would
# leave an account nobody can sign in to.
read -rs "A?Password: "; echo; read -rs "B?Again: "; echo
[ "$A" = "$B" ] && export ADDUSER_PASSWORD="$A" || echo "they differ"; unset A B
go run ./cmd/adduser -dsn "$DB_DSN" -email mum@example.com -name "Mum"
go run ./cmd/adduser -dsn "$DB_DSN" -email me@example.com -name "Me" -role admin
go run ./cmd/adduser -dsn "$DB_DSN" -email mum@example.com -reset   # forgotten password
unset ADDUSER_PASSWORD
```

Passwords are 8 to 72 bytes. `-reset` also signs that account out everywhere.
There is no email yet, so this is also how a forgotten password is dealt with.

### 5. Start Server

```bash
go run ./cmd/api
```

Server will start at `http://localhost:8080`

---

## Docker

### Run everything with Docker Compose

```bash
docker compose up -d
```

This will start both the database and the API server. The database port is
published on `127.0.0.1` only.

### Production

[docs/deploy.md](docs/deploy.md) walks through a free-tier deployment (Cloud
Run + Neon + R2). With `APP_ENV=production` the server refuses to start on a
default or short `JWT_SECRET`, the local development database, or a wildcard
CORS origin, never prints password-reset tokens, and sends no CORS headers
unless `CORS_ALLOW_ORIGIN` names an origin.

---

## Makefile Commands

```bash
make dev              # Everyday: apply new migrations, then run the API with demo cameras and appliances
make migrate          # Run migrations up
make migrate-down     # Rollback last migration
make migrate-reset    # Reset all migrations
make migrate-status   # Show migration status
make seed             # Seed database
make fresh            # Reset + migrate + seed
make run              # Run API server
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `APP_ENV` | `production` makes unsafe settings (below) a start-up error, and keeps password-reset tokens out of the log | `development` |
| `APP_PORT` | Server port (falls back to `PORT`, which cloud hosts set) | `8080` |
| `DB_DSN` | PostgreSQL connection string. Production refuses the local default | `postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable` |
| `JWT_SECRET` | JWT signing secret. Production needs 32+ characters (`openssl rand -hex 32`) | `change-me` |
| `CORS_ALLOW_ORIGIN` | The one browser origin allowed. The app is native and needs none: leave it unset in production (no CORS headers at all). `*` is refused in production | `*` in development, none in production |
| `TRUSTED_PROXY_HOPS` | How many reverse proxies sit in front and add to `X-Forwarded-For` (Cloud Run, Caddy: 1). Decides which address the rate limits see; too high lets a caller choose their own, 0 ignores the header | `0` |
| `AUTH_RATE_LIMIT_PER_MIN` | Calls per minute per address on login/refresh/forgot/reset (burst 20). `0` = off. Wrong passwords are limited separately: 10 per account then one per 90 s, 20 per address then one per 45 s | `60` |
| `STORAGE_BACKEND` | Storage backend (`local` or `r2`) | `local` |
| `LOCAL_STORAGE_DIR` | Local storage directory | `./data/uploads` |
| `PUBLIC_BASE_URL` | Public URL for file access | `http://localhost:8080/static` |
| `R2_ACCOUNT_ID` | Cloudflare R2 Account ID | - |
| `R2_ACCESS_KEY_ID` | Cloudflare R2 Access Key | - |
| `R2_SECRET_ACCESS_KEY` | Cloudflare R2 Secret Key | - |
| `R2_BUCKET` | Cloudflare R2 Bucket name | - |
| `R2_PUBLIC_URL` | Cloudflare R2 Public URL | - |
| `GOOGLE_PLACES_API_KEY` | Google Places API (New) key. Empty = Google lookups off | - |
| `GOOGLE_PLACES_BASE_URL` | Override Google's endpoint (tests / local fake) | Google production |
| `GOOGLE_PLACES_DAILY_LIMIT` | Outbound Google calls per day, all users (0 = unlimited) | `300` |
| `GOOGLE_PLACES_RATE_PER_MIN` | Google-backed requests per user per minute | `20` |
| `GOOGLE_PLACES_MAX_RADIUS_M` | Largest search radius accepted (Google's own cap is 50000) | `20000` |
| `GOOGLE_PLACES_ALLOW_ALL_TRADES` | Allow a search with no trade and no text (fans out to 4 calls) | `false` |
| `IMOU_APP_ID` / `IMOU_APP_SECRET` | Imou Open Platform credentials (open.imoulife.com). Both set = cameras on | _(empty = off)_ |
| `IMOU_REGION` | `sg` picks the Singapore host; for other regions set `IMOU_BASE_URL` | _(global host)_ |
| `IMOU_BASE_URL` | Override the Imou API host (e.g. `https://openapi-xx.easy4ip.com/openapi`) | region default |
| `CAMERAS_DRIVER` | `imou`, `fake` (demo cameras, no account needed) or empty (auto) | _(auto)_ |
| `CAMERAS_FAKE_STREAM_URL` | HLS address the fake driver hands out | Apple's test stream |
| `APPLIANCES_DRIVER` | `local` = appliances the household adds itself, simulated here (what `make dev` uses); `fake` = a fixed demo set (tests); empty = appliance control off. Real vendors are added as drivers | _(empty = off)_ |
| `CAMERAS_FAKE_PICTURES` | `true` = the fake cameras give a made-up still picture; otherwise none, so the app shows a camera icon on the cover | `false` |

---

## API Endpoints

### Auth (Public)
| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/auth/login` | Login (answers 201). Wrong attempts are rate limited: 429 with `Retry-After` |
| POST | `/api/v1/auth/refresh` | Trade a **refresh** token for a new pair (an access token is refused) |
| POST | `/api/v1/auth/forgot-password` | Always answers 200. No email is sent yet: development logs a token; production makes none (use `cmd/adduser -reset`) |
| POST | `/api/v1/auth/reset-password` | Reset password with a token (single use, one hour) |

There is no sign-up: see [Real accounts](#real-accounts-sign-up-is-closed).
The login routes share a per-address cap (`AUTH_RATE_LIMIT_PER_MIN`).

Tokens come in two kinds, told apart by their `typ` claim. An **access** token
(24 h) opens the API; a **refresh** token (7 d) opens only `/auth/refresh`.
Each carries the account's `token_version`; changing or resetting a password
raises it, which ends every earlier token at once. Changing a password
therefore answers with a fresh `access_token`/`refresh_token` for the device
that asked, which the app should store. `POST /auth/logout` only acknowledges:
the app discards its tokens.

### User (Auth Required)
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/me` | Get current user |
| PATCH | `/api/v1/me` | Update profile |
| POST | `/api/v1/me/password` | Change password (checks the old one; answers with fresh tokens) |
| POST | `/api/v1/auth/logout` | Acknowledge a logout (the app discards its tokens) |

Every endpoint that needs auth answers `401 {"error":"account_not_found"}` when
the token is valid but its account no longer exists (deleted, or the database
was reset under a running app), and `401 {"error":"session_revoked"}` when the
password has been changed since the token was issued; the app then signs out.
A user found is remembered for a minute, so this is not a query per request
(a change made here takes effect at once).

### Limits and safety

- Request bodies are capped at 1 MiB (uploads: 26 MiB) — `413` beyond that.
- Errors from the database never reach the caller: they answer a fixed
  `internal error` (500) or `invalid input` (400) and the detail goes to the log.
- Uploads accept JPEG, PNG, GIF, WebP, HEIC/HEIF, MP4 and PDF only; the file's
  first bytes must match the claimed type, and the stored name keeps nothing
  from the client but a plain-character stem.
- Searches treat `%` and `_` literally and use at most 100 characters.

### Notes
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/notes` | List notes |
| POST | `/api/v1/notes` | Create note |
| GET | `/api/v1/notes/:id` | Get note |
| PUT | `/api/v1/notes/:id` | Update note |
| DELETE | `/api/v1/notes/:id` | Delete note |
| POST | `/api/v1/notes/:id/pin` | Pin note |
| POST | `/api/v1/notes/:id/unpin` | Unpin note |
| POST | `/api/v1/notes/:id/done` | Mark done |
| POST | `/api/v1/notes/:id/undone` | Mark undone |

### Chores
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/chores` | List chores |
| POST | `/api/v1/chores` | Create chore |
| POST | `/api/v1/chores/:id/claim` | Claim chore |
| POST | `/api/v1/chores/:id/complete` | Complete chore |
| DELETE | `/api/v1/chores/:id` | Delete chore |

### Bills
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/bills` | List bills |
| POST | `/api/v1/bills` | Create bill |
| GET | `/api/v1/bills/:id` | Get bill |
| PATCH | `/api/v1/bills/:id` | Update bill |
| DELETE | `/api/v1/bills/:id` | Delete bill |
| POST | `/api/v1/bills/:id/pay` | Mark as paid |
| GET | `/api/v1/bills/summary` | Bills summary |

### Purchases
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/purchases` | List purchases |
| POST | `/api/v1/purchases` | Create purchase |
| GET | `/api/v1/purchases/:id` | Get purchase |
| PATCH | `/api/v1/purchases/:id` | Update purchase |
| DELETE | `/api/v1/purchases/:id` | Delete purchase |
| POST | `/api/v1/purchases/:id/claim` | Claim purchase |
| POST | `/api/v1/purchases/:id/progress` | Progress status |
| POST | `/api/v1/purchases/:id/cancel` | Cancel purchase |

### Medicine
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/medicine` | List medicine |
| POST | `/api/v1/medicine` | Add medicine |
| GET | `/api/v1/medicine/:id` | Get medicine |
| PATCH | `/api/v1/medicine/:id` | Update medicine |
| DELETE | `/api/v1/medicine/:id` | Delete medicine |
| POST | `/api/v1/medicine/:id/stock` | Adjust stock |
| GET | `/api/v1/medicine/:id/alert` | Get alert |
| PUT | `/api/v1/medicine/:id/alert` | Set alert |
| GET | `/api/v1/medicine/low-stock` | List low stock |
| GET | `/api/v1/medicine/expiring` | List expiring soon |
| GET | `/api/v1/medicine/expired` | List expired |

### Contractors
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/contractors` | List contractors |
| POST | `/api/v1/contractors` | Add contractor |
| GET | `/api/v1/contractors/:id` | Get contractor |
| PATCH | `/api/v1/contractors/:id` | Update contractor |
| DELETE | `/api/v1/contractors/:id` | Delete contractor |
| POST | `/api/v1/contractors/:id/favorite` | Toggle favorite |
| GET | `/api/v1/contractors/favorites` | List favorites |

### Places (Google, optional)
Off unless `GOOGLE_PLACES_API_KEY` is set; then Google-backed calls answer
`503 google_places_disabled`. Auth required.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/places/status` | `{enabled, daily_used, daily_limit}`. Always answers, costs no Google call |
| GET | `/api/v1/places/nearby?lat=&lng=&radius=&type=&q=&limit=` | Businesses near a point, nearest first. Needs `type` (`electrician`, `plumber`, `carpenter`, `hvac`) or `q`; each result has `starred` |
| GET | `/api/v1/places/starred?lat=&lng=` | The household's starred places, looked up by id (1 cheap Google call each, max 20) |
| GET | `/api/v1/places/:place_id` | Phone, rating, website, opening status for one place |
| PUT | `/api/v1/places/:place_id/star` | Star a place. Body `{"trade":"plumber"}` is optional. Works with Google off |
| DELETE | `/api/v1/places/:place_id/star` | Remove the star |

How the load is kept down:
- A search without a trade or text is refused (`400 type_or_q_required`), so
  "browse all" never reaches Google.
- One search = one Google call; results per search default to 20, max 40;
  radius is capped (default 20 km).
- Per user: 20 requests/min (`429 rate_limited`). Across everyone: a daily
  call budget (`429 daily_limit`), reset at midnight Asia/Bangkok. It is
  in-memory, so a restart resets it; move it to the database if it ever has
  to be a hard financial limit across several instances.
- A fan-out is reserved all-or-nothing, so a refused request never half-spends.

Notes: only a `place_id` (and our own trade label) is stored, never Google's
content; the list call requests cheap fields and phone/rating/website is a
separate call because those fields bill at a higher rate; Google's terms don't
allow these results on a non-Google map, so the app lists them without pinning
them on OpenStreetMap.

### Cameras (Imou, optional)
Off unless `IMOU_APP_ID` + `IMOU_APP_SECRET` are set (or `CAMERAS_DRIVER=fake`);
then it answers `503 cameras_disabled`. Auth required. The video and its
recordings stay with Imou: the server only asks Imou's cloud for a live (HLS)
address and a still picture and hands those to the app, so it never relays
video.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/cameras/status` | `{enabled, driver}`. Always answers, costs no Imou call |
| GET | `/api/v1/cameras` | `{items:[{id,name,model,online,human_detect,ptz,zoom,switches}]}`. `?refresh=1` skips the 60 s cache. `ptz`/`zoom`/`switches` come from the camera's own abilities, so the app shows only controls that work |
| GET | `/api/v1/cameras/:id/live?quality=hd\|sd` | `{url, protocol:"hls", quality}`. Never cached |
| GET | `/api/v1/cameras/:id/snapshot` | A still picture (image bytes), private-cached for 10 s |
| POST | `/api/v1/cameras/:id/ptz` | Body `{"action":"left","duration_ms":600}`. Actions: `up down left right up_left up_right down_left down_right zoom_in zoom_out stop`. Duration is clamped to 0.1–3 s (default 0.5 s), so a lost "stop" can't leave the camera swinging |
| GET | `/api/v1/cameras/:id/switches` | `{"privacy":false,"motion":true,"human":true}` for the settings this camera has |
| PUT | `/api/v1/cameras/:id/switches/:key` | Body `{"on":true}`. Keys: `privacy` (view off), `motion`, `human`, `light` (the camera's own light, Imou `whiteLight`), `light_auto` (light on motion, `linkageWhiteLight`) |

Errors: `429 provider_limit` (Imou itself refused: its own call allowance is
used up), `409 camera_offline`,
`404 camera_not_found`, `422 not_supported` (the camera lacks that
capability), `503 cameras_misconfigured` (credentials rejected),
`502 cameras_unavailable`.

There is no rate limit, call budget or audit log on our side: this is one
household. Imou has its own allowance per app (reported as ~30,000 requests a
month); the server only keeps calls down by caching lists, snapshots and live
addresses for 60 / 20 / 30 s, and by not calling for a camera already known to
be offline.

The Imou client follows the protocol of the open-source `imouapi` library
(signed `POST {base}/{method}`; `accessToken`, `deviceBaseList`,
`deviceBaseDetailList`, `deviceOnline`, `getLiveStreamInfo`, `bindDeviceLive`,
`setDeviceSnapEnhanced`, `controlMovePTZ`, `getDeviceCameraStatus`,
`setDeviceCameraStatus`). Abilities are read from the device's `ability`
string (`PTZ`/`PT`, `ZoomFocus`, `CloseCamera`, `MotionDetect`,
`HeaderDetect`/`AiHuman`, `WhiteLight`, `Linkagewhitelight`). That is not Imou's own documentation: confirm
against open.imoulife.com when the account is set up.

### Appliances (optional)
Off unless `APPLIANCES_DRIVER` names a driver; then it answers
`503 appliances_disabled`. Auth required. The driver is the source of truth for
what exists and what state it is in; the server checks each command, relays it,
reads the state back and logs who sent it.

Drivers: **`local`** keeps the appliances the household adds itself (table
`registered_appliances`) and simulates them, so nothing real is controlled; it
is how an appliance is tried out before a vendor driver exists. **`fake`** is a
fixed set of five, for tests.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/appliances/status` | `{enabled, driver, can_add}`. Always answers, costs no vendor call. `can_add`: the household can add appliances itself |
| POST | `/api/v1/appliances` | Add one (admin only). Body `{"name","kind","room","brand","model","connection"}`; `kind`: `aircon`, `cooler`, `light`, `fan`, `plug`, `heater`, `other`; `connection`: `demo` (the only one that exists so far). Answers `201` with the appliance. `422 not_supported` when the driver has a fixed list |
| DELETE | `/api/v1/appliances/:id` | Remove one the household added (admin only), `204` |
| GET | `/api/v1/appliances` | `{items:[{id,name,kind,room,brand,model,connection,removable,online,status,checked_at,since,auto_off_at,capabilities,state}]}` (see **Status** below). `?refresh=1` skips the 5 s cache |
| GET | `/api/v1/appliances/:id` | One appliance |
| POST | `/api/v1/appliances/:id/command` | Body `{"capability":"power","value":true}`. Answers `{id, state, confirmed}` with the state read back from the appliance |
| GET | `/api/v1/appliances/:id/events?limit=` | Who sent what, newest first (default 50, max 200) |

- **Capabilities** tell the app what to draw and bound what is accepted:
  `{key, type:"bool"|"int"|"enum", min, max, unit, options, admin_only}`. Keys
  the app knows: `power`, `brightness`, `temperature`, `mode`, `fan_speed`,
  `swing`, `cooling`. An air conditioner added through `local` has `power`,
  `temperature` (16-30 °C), `mode` (`auto`, `cool`, `dry`, `fan`, `heat`),
  `fan_speed` (1-5) and `swing`; an air cooler (`cooler`, a portable
  evaporative one) has `power`, `fan_speed` (1-3), `swing` (the louvers sweep
  left and right), `cooling` (the water pump) and `mode` (`normal`, `natural`,
  `sleep`).
- **Status** is decided by the server, so every screen agrees: `status` is
  `on` (reachable and on), `off` (reachable and off) or `offline` (cannot be
  reached; `state` is the last one known), the words smart-home apps use. `checked_at` is when
  `state` was read from the appliance (up to 5 s old, the cache).
  `since` is when it was switched to how it is now, from the event log; it is
  left out when that isn't known (switched some other way, e.g. a wall switch,
  or never switched through us). `auto_off_at` is when the safety switch-off
  will fire, while a heater is on. The app checks the list again every 10 s
  while an appliance screen is open.
- **What a connection needs** (shown by the app when adding): `wifi` — an air
  conditioner with Wi-Fi, a 2.4 GHz network, the maker's app account and a
  server driver for that maker; `ir` — a Wi-Fi IR blaster in view of the
  air conditioner, whose code library has the model (IR is one-way, so the real
  state cannot be read); `homeassistant` — a Home Assistant with the air
  conditioner as a climate device and a long-lived token. None of these has a
  driver yet.
- **Who may do what:** any signed-in member can switch an appliance on and off
  (`power`); every other control is `admin_only` (`403 admin_only`).
- **Values:** an `int` outside `min`..`max` is brought inside it (the answer
  shows the value that was set); a fraction, or the wrong type, is
  `400 invalid input`.
- **Safety switch-off:** a heater switched on is switched off by the server
  after 2 hours (logged with `source: "auto_off"`), in case the connection
  drops. The timer lives in the API process, so a restart forgets it.
- Errors: `409 appliance_offline` (no vendor call is made), `404
  appliance_not_found`, `422 not_supported` (the appliance lacks that
  capability), `429 provider_limit`, `503 appliances_misconfigured`,
  `502 appliances_unavailable`.

### Dashboard
Where the money went: what the bills and purchases features add up to. Auth
required.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/dashboard/spending?month=YYYY-MM` | The month (default: this one) in one answer. `400` for a bad month |

The answer has `total_spent` (bills paid + purchases bought in the month) with
`previous_spent` for "up or down on last month"; `bills` (`paid`, `by_type`,
and what is `unpaid` / `overdue` right now); `purchases` (`spent`,
`by_category`, what is still `open_*`); a six-month `trend` (oldest first, ending
with the month); and the next five `upcoming` unpaid bills with `days_left`
(negative once overdue).

- **Which month a sum belongs to:** a bill by the day it was **paid**
  (`paid_at`), a purchase by the day it was **bought** (`bought_at`). The
  database stamps both once, whichever way a bill becomes `paid` or a purchase
  `bought`/`delivered` (migrations 00015 and 00016). Months are calendar months
  in Thailand (UTC+7).
- **Day by day:** `daily` and `previous_daily` list every day of the month and
  of the month before (bills and purchases per day), and `through_day` says how
  far the month has got, so the app can draw the two running totals together.
- **Currency:** totals are in baht. A purchase in another currency is counted in
  `purchases.excluded_count`, not added.

**Sample data for the dashboard.** `make seed` / `make fresh` also add six
months of paid bills and bought purchases, every one marked "(demo)" in its
title, so the charts have something to draw. That part replaces its own earlier
rows instead of adding to them. `make demo-spending` makes just those rows and
`make demo-spending-clear` removes them (`go run ./cmd/demospend -dsn=… [-clear]`:
it has no default database and writes only to this machine's).

### Files
| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/uploads` | Upload file |
| GET | `/api/v1/files/:id` | Get file |
| DELETE | `/api/v1/files/:id` | Delete file |

### Health
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/healthz` | Liveness check (not reachable on a Cloud Run `run.app` address: Google's front end answers that path itself; use `/readyz` there) |
| GET | `/readyz` | Readiness check (does not touch the database) |

---

## Database Schema

| Table | Description |
|-------|-------------|
| `users` | User accounts with roles |
| `notes` | Notes and tasks |
| `chores` | Household chores |
| `bills` | Monthly bills |
| `purchases` | Purchase requests |
| `medicine_items` | Medicine stock |
| `medicine_alerts` | Medicine alerts |
| `contractors` | Contractors list |
| `starred_places` | Google place ids the household starred |
| `files` | Uploaded files |
| `purchase_attachments` | Files attached to a purchase |
| `purchases.bought_at` | (column) when a purchase was bought; set once by a trigger, used by the dashboard |
| `registered_appliances` | Appliances the household added itself, with their state (`local` driver) |
| `appliance_events` | Who switched or adjusted which appliance |
| `password_resets` | Password reset tokens |

---

## Testing

```bash
go test ./... -race                       # unit tests (no database needed)
TEST_DB_DSN='postgres://…' go test ./...  # + the tests that need a migrated database
```

`go test ./internal/e2e/` runs every main feature (auth, notes, chores, bills,
purchases, medicine, contractors, files, places, cameras, appliances) over
HTTP against a **running** API. It is skipped unless `E2E_API_URL` is set, and
it creates users and data, so point it at a throwaway server and database
only. Sign-up is closed, so it makes its accounts directly in that database:
`E2E_DB_DSN` must be the DSN the throwaway API runs on. Raise
`AUTH_RATE_LIMIT_PER_MIN` for the run (the suite signs in more often than a
person does):

```bash
# terminal 1: the throwaway API (DB_DSN must be the throwaway database)
DB_DSN='postgres://…' AUTH_RATE_LIMIT_PER_MIN=600 CAMERAS_DRIVER=fake APPLIANCES_DRIVER=fake go run ./cmd/api
# terminal 2 (the admin account is optional; `DB_DSN=… go run ./cmd/seed` makes one)
E2E_API_URL=http://localhost:8080 E2E_DB_DSN='postgres://…' \
E2E_ADMIN_EMAIL=admin@example.com E2E_ADMIN_PASSWORD=admin2026 \
  go test ./internal/e2e/ -v
```

`cmd/seed` takes its database from `-dsn`, then the `DB_DSN` environment
variable, then the local `homeservice` database. `cmd/seed`, and
`cmd/migrate -cmd=down|reset`, refuse a database that is not on this machine
(and `APP_ENV=production`) unless you pass `-allow-remote`, so a `DB_DSN` left
in your shell from a deploy cannot be wiped by `make fresh`.

---

## License

No license has been chosen yet, so all rights are reserved.