# Deploying the server on free tiers

The stack, and what it costs a month while the household stays inside the free
limits:

| Part | Service | Cost | Limit that matters |
|---|---|---|---|
| API | Google Cloud Run, region `asia-southeast1` (Singapore) | $0 | 2 M requests, 180 k vCPU-seconds, 360 k GB-seconds a month; Singapore's compute allowance is smaller and **not confirmed** (see below) |
| Database | Neon Free, project `homehq`, region `aws-ap-southeast-1` | $0 | 100 compute-hours, 1 GB storage (Neon's pages disagree: one says 0.5 GB), 5 GB transfer a month; compute sleeps after 5 idle minutes; 6 hours of history |
| Files | Cloudflare R2 | $0 | 10 GB stored (Cloudflare's published free tier, not rechecked here) |
| Secrets | Google Secret Manager | $0 | 6 active secret versions, 10 000 accesses a month |
| Sign-in | the server's own (JWT + bcrypt) | $0 | 3 accounts, no email |

Not free, so watch: **Cloud Run's data out of Singapore** (the free 1 GB is for
North America only; JSON and a few photos should stay at cents, figure not
verified) and **a card on file** for Google Cloud (and possibly for R2; not
checked). Building the image uses Cloud Build and Artifact Registry: a small
image rebuilt a few times a year should fit their free allowances (Artifact
Registry: 0.5 GB per Google's page; Cloud Build's allowance not rechecked).
Set the spending cap (step 8) before anything else.

If Neon runs out of compute hours, the database is suspended until the next
month: the app is down, nothing is lost. Nothing in the server keeps the
database awake on its own (`/healthz` and `/readyz` never touch it; the pool
closes idle connections after two minutes and never redials in the
background), so hours are spent only while the app is used.

## What the host changes in how the server behaves

- **Scale to zero.** The instance is stopped when idle, so the first request
  after a quiet spell takes a second or two longer, and everything kept in
  memory is forgotten: the camera caches, the rate limits, the password-guess
  counters. Deploy with `--max-instances 1` so the limits are not split across
  copies.
- **CPU only during a request.** After a response is sent the CPU is held back,
  so work started in the background may stall. The planned camera-alert
  receiver must therefore do its work before it answers (or hand it to a queue
  that calls back in), not after.
- **Local files vanish.** `STORAGE_BACKEND` must be `r2`; the server warns at
  start-up if it is `local` in production.

## Steps (you run these; the server never needs your keys in a chat)

### 1. Database (Neon)

Create a **new project** `homehq` (Postgres 16, region AWS Singapore). Leave the
existing project alone. Copy the **direct** connection string (not the
`-pooler` one) and keep it in a shell variable, not in a file or a chat:

```bash
read -rs DB_DSN && export DB_DSN        # paste the postgres://… string
go run ./cmd/migrate -cmd=up            # applies the schema; safe on a remote database
```

`migrate -cmd=down|reset` and `seed` refuse a remote database on purpose.

### 2. Accounts

Sign-up is closed, so make the household's accounts by hand (see the README's
"Real accounts"). The password is typed into a variable, never a flag:

```bash
# type it twice: nothing is echoed, so a typo would be invisible
read -rs "A?Password: "; echo; read -rs "B?Again: "; echo
[ "$A" = "$B" ] && export ADDUSER_PASSWORD="$A" || echo "they differ"; unset A B
go run ./cmd/adduser -dsn "$DB_DSN" -email you@example.com -name "You" -role admin
```

### 3. Files (R2)

Create a bucket, an API token limited to that bucket (Object Read & Write), and
its public address. You will need: account id, bucket name, public URL, access
key id and secret.

### 4. Google Cloud project

```bash
gcloud auth login
gcloud projects create homehq-<something-unique>       # or reuse a project
gcloud config set project <that-id>
gcloud services enable run.googleapis.com cloudbuild.googleapis.com \
  artifactregistry.googleapis.com secretmanager.googleapis.com
```

Link a billing account to the project in the console (required even for free
use), then do step 8 (the spending cap) before continuing.

### 5. Secrets

The values are generated or typed here, never put in a file. Create the Google
Places key **in this project** (enable Places API (New) here, restrict the key to
it): its usage is billed to the key's project, so a key from an older project
would sit outside the spending cap in step 8.


```bash
printf '%s' "$(openssl rand -hex 32)" | gcloud secrets create jwt-secret --data-file=-
printf '%s' "$DB_DSN"                  | gcloud secrets create db-dsn --data-file=-
read -rs V; printf '%s' "$V" | gcloud secrets create r2-secret-access-key --data-file=-
read -rs V; printf '%s' "$V" | gcloud secrets create r2-access-key-id --data-file=-
# only if used:
read -rs V; printf '%s' "$V" | gcloud secrets create imou-app-secret --data-file=-
read -rs V; printf '%s' "$V" | gcloud secrets create google-places-api-key --data-file=-
unset V
```

That is six secrets, exactly the free allowance of six active versions: delete
(or destroy) an old version when you rotate one.

The web service gets its **own service account** that can read only these
secrets. Do not let it run as the project's default account: Google gives that
one the broad `Editor` role (seen on a fresh project), which a bug in the API
could then use against the whole project.

```bash
PROJECT=$(gcloud config get-value project)
gcloud iam service-accounts create homehq-api --display-name="HomeHQ API"
for s in jwt-secret db-dsn r2-secret-access-key r2-access-key-id imou-app-secret google-places-api-key; do
  gcloud secrets add-iam-policy-binding "$s" \
    --member="serviceAccount:homehq-api@${PROJECT}.iam.gserviceaccount.com" \
    --role=roles/secretmanager.secretAccessor
done
```

### 6. Deploy

From the server folder (the `Dockerfile` is used; `.dockerignore` keeps your
local files out of the build):

```bash
gcloud run deploy homehq-api --source . --region asia-southeast1 \
  --service-account homehq-api@$(gcloud config get-value project).iam.gserviceaccount.com \
  --allow-unauthenticated --max-instances 1 --min-instances 0 \
  --cpu 1 --memory 256Mi --timeout 60 \
  --set-env-vars APP_ENV=production,TRUSTED_PROXY_HOPS=1,STORAGE_BACKEND=r2,R2_ACCOUNT_ID=<id>,R2_BUCKET=<bucket>,R2_PUBLIC_URL=<https://…>,CAMERAS_DRIVER=imou,IMOU_APP_ID=<id>,IMOU_REGION=sg \
  --set-secrets JWT_SECRET=jwt-secret:latest,DB_DSN=db-dsn:latest,R2_SECRET_ACCESS_KEY=r2-secret-access-key:latest,R2_ACCESS_KEY_ID=r2-access-key-id:latest,IMOU_APP_SECRET=imou-app-secret:latest,GOOGLE_PLACES_API_KEY=google-places-api-key:latest
```

Leave out the Imou/Google lines you do not use. `--allow-unauthenticated` only
means Google does not add its own login: the app's own sign-in protects every
route. In production the server **refuses to start** with a default or short
`JWT_SECRET`, the local database, or `CORS_ALLOW_ORIGIN=*`.

The command prints the service address (`https://homehq-api-….run.app`). Put
it in the app as `API_BASE_URL`.

### 7. Check it

```bash
URL=https://homehq-api-….run.app
curl -s "$URL/readyz"                                   # ready
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$URL/api/v1/auth/register" \
  -H 'Content-Type: application/json' -d '{}'           # 404: sign-up is closed
curl -s -o /dev/null -w '%{http_code}\n' "$URL/api/v1/me"   # 401: sign-in needed
```

Use `/readyz`, not `/healthz`: on a `run.app` address Google's front end answers
`/healthz` itself with its own 404 page before the request reaches the server
(seen on the first deploy). `/readyz` never touches the database, so it does not
wake Neon.

**Checked on the first deploy (2026-10-24): `TRUSTED_PROXY_HOPS=1` is right for
Cloud Run.** The address the app logs (`from …` on each request line) was the
same as the one in Cloud Run's own request log (`httpRequest.remoteIp`), i.e.
the caller's real address, not a Google one. To repeat the check:

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="homehq-api" AND httpRequest.requestUrl:"/readyz"' --limit=1 --format='value(httpRequest.remoteIp)'
gcloud run services logs read homehq-api --region=asia-southeast1 --limit=40 | grep '/readyz HTTP'
```

With a wrong value every visitor would look like one address, and a handful of
wrong passwords from anyone would lock everyone out for a minute.

### 8. Spending cap (do this before step 6, really)

A budget **alert** only sends an email, and Google says it is delayed and does
not guarantee you stay under the amount. What stops spending is a function that
switches billing off for the project when the budget is exceeded. Nothing else
in this stack does that for Google Cloud, so set it up once, test it, then
deploy.

**What it protects, and what it does not**

| Service | Stops by itself at the free limit? |
|---|---|
| Neon Free | Yes: compute is suspended, no overage is billed (Neon's FAQ) |
| Google Cloud (Cloud Run, builds, secrets, data out) | **No**, until this function exists |
| Google Places | Only if its key belongs to **this** project (create the key here, not in an older project: usage is billed to the key's project, which this cap cannot see). The app also caps itself: `GOOGLE_PLACES_DAILY_LIMIT` |
| Cloudflare R2 | Not verified: no spending cap known. The app limits each file to 25 MiB and needs a login |
| Imou | Not verified: decide when it is switched on |

**What switching billing off does:** every service in the project stops, free
ones included, and Google warns that resources "might be irretrievably
deleted" and that recovery is not guaranteed. That is why the data lives
outside the project (database on Neon, files on R2): if the project is lost,
create a new one, recreate the secrets (a fresh `jwt-secret` signs everyone
out once) and deploy again. To restart after a cut-off, link billing to the
project again in the console and redeploy.

**What triggers it:** the budget below counts cost **after free-tier credits
only** (`FREE_TIER`), not after trial credits, so it fires when you go past the
free allowance even while trial money would still cover it. Budget status goes
to Pub/Sub several times a day; the function switches billing off when
`costAmount` is above `budgetAmount`. Cost data arrives late, so a little can
be spent before it fires. Choose the amount so that is acceptable. The budget
uses your billing account's currency (`gcloud billing accounts describe
$BILLING_ACCOUNT --format=json` shows `currencyCode`; a request in another
currency is rejected with an unhelpful `INVALID_ARGUMENT`). A very low amount
cuts the app off for cents of build or network cost: ฿100 (about $3) leaves
room for that and is what this project uses.

```bash
PROJECT=$(gcloud config get-value project)
BILLING_ACCOUNT=<ID from: gcloud billing accounts list>
BUDGET_AMOUNT=100THB       # the billing account's own currency
SA=billing-killswitch@${PROJECT}.iam.gserviceaccount.com

gcloud services enable cloudbilling.googleapis.com pubsub.googleapis.com \
  cloudfunctions.googleapis.com eventarc.googleapis.com billingbudgets.googleapis.com

# its own service account: the web service must never hold billing powers
gcloud iam service-accounts create billing-killswitch --display-name="HomeHQ billing kill switch"
gcloud pubsub topics create billing-budget

# starts in SIMULATE mode: it only logs what it would do
gcloud functions deploy stop-billing --gen2 --runtime=python312 --region=asia-southeast1 \
  --trigger-topic=billing-budget --entry-point=stop_billing \
  --source=deploy/billing-killswitch --service-account="$SA" \
  --set-env-vars TARGET_PROJECT=$PROJECT,SIMULATE=true \
  --max-instances=1 --memory=256Mi

# roles the Pub/Sub trigger needs
for r in roles/eventarc.eventReceiver roles/run.invoker roles/logging.logWriter; do
  gcloud projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:$SA" --role=$r --condition=None
done

# The narrow role that switches billing off: it may change THIS project's
# billing link and nothing else. (Tested: with only this role a live message
# switched billing off within seconds. It cannot READ the billing state, so the
# log shows one harmless warning line first.) Google's own guide uses the much
# broader roles/billing.admin on the billing account, which can also attach
# other projects to your card: keep that as the fallback, not the first choice.
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:$SA" --role=roles/billing.projectManager --condition=None

gcloud billing budgets create --billing-account="$BILLING_ACCOUNT" \
  --display-name="homehq cap" --budget-amount=$BUDGET_AMOUNT --calendar-period=month \
  --filter-projects=projects/$PROJECT \
  --credit-types-treatment=include-specified-credits --filter-credit-types=FREE_TIER \
  --threshold-rule=percent=0.5 --threshold-rule=percent=1.0 \
  --notifications-rule-pubsub-topic=projects/$PROJECT/topics/billing-budget
```

If the function deploy complains about a permission, grant the role it names to
the `billing-killswitch` account.

**Test it before you rely on it** (done on the first deploy, 2026-10-24, in
this order)

1. Simulation: publish fake notifications and read the log.

   ```bash
   gcloud pubsub topics publish billing-budget --message='{"costAmount": 5, "budgetAmount": 1}'
   gcloud pubsub topics publish billing-budget --message='{"costAmount": 0.5, "budgetAmount": 1}'
   gcloud pubsub topics publish billing-budget --message='not a notification'
   gcloud functions logs read stop-billing --region=asia-southeast1 --gen2 --limit=40
   ```

   Expected: `SIMULATION: would switch billing off…`, then
   `no action: cost 0.5 is within the budget 1.0`, then `no action: unreadable
   notification`.
2. The real cut-off, while the project holds nothing worth keeping (before the
   first deploy): flip to live, send the over-budget fake, watch billing go off,
   and link it back at once.

   ```bash
   gcloud run services update stop-billing --region=asia-southeast1 --update-env-vars SIMULATE=false
   gcloud pubsub topics publish billing-budget --message='{"costAmount": 9, "budgetAmount": 1}'
   gcloud billing projects describe "$PROJECT" --format='value(billingEnabled)'   # False within seconds
   gcloud billing projects link "$PROJECT" --billing-account="$BILLING_ACCOUNT"   # back on
   ```

   Expected log line: `BILLING SWITCHED OFF for projects/… (cost 9.0 exceeds the budget 1.0)`.
   Once the function is live, **never publish a fake over-budget message**: it
   does what it says.
3. The budget also emails the billing admins at 50 % and 100 %.

(`gcloud functions deploy --update-env-vars` alone does not work: it asks for the
source again, so the flip goes through `gcloud run services update`, which is
the service behind the function.)

## Backups

Neon Free keeps only 6 hours of history. A dump to R2 on a schedule is not set
up yet: before the household relies on this, add a weekly `pg_dump` job (a
GitHub Actions cron that writes to a private R2 bucket fits the free tiers) and
try restoring one.

## Data retention

Rows are small: a household adding 50 a day for four years is on the order of
tens of MB, far inside 1 GB (an estimate, not measured). Photos in R2 are what
fill a free tier: a 2 MB photo a day is about 2.9 GB over four years, and
deleting a file in the app currently removes only its database row, not the R2
object. Both belong in a retention job (delete rows and objects older than N
years); neither is built yet.

## If the free allowances change

Everything above is a snapshot from 2026-10-18. Cloud Run in Singapore, the
Neon free plan and R2 can all change; recheck the vendors' own pricing pages
before relying on a figure here.
