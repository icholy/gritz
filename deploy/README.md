# Deploying gritz to DigitalOcean

The server runs on a single DigitalOcean droplet managed by
[Dokku](https://dokku.com), with Postgres as a container on that same droplet
(`dokku-postgres`), replacing the Fly.io app plus its separate `xagent-db`
Postgres app.

[`cloud-init.yaml`](cloud-init.yaml) does the host provisioning. It is committed
to the repo and contains no secrets, so it only takes the host as far as "empty
app, waiting for its first deploy". Everything that needs a credential, or needs
DNS to already resolve, is a manual step below.

## What cloud-init sets up

- 2G swap and `vm.swappiness=10`
- `ufw` allowing 22, 80 and 443
- Dokku v0.38.27 (which installs Docker)
- `dokku-postgres` 1.48.0 and `dokku-letsencrypt` 0.25.2, both pinned
- The `gritz` app, the `gritz-db` Postgres service, and the link between them
  exposed as `GRITZ_DATABASE_URL` with `sslmode=disable`
- `dokku ports:set gritz http:80:6464`, matching `EXPOSE 6464` in the image
- Every SSH key DigitalOcean injected into root, re-added as a Dokku admin key

The plugins are pinned deliberately: `dokku-postgres` takes its Postgres version
from its own checkout, so tracking `master` would silently move the database
version if this host were ever rebuilt. 1.48.0 ships Postgres 18.4, matching
`docker-compose.yml`.

## 1. Create the droplet

```bash
export DROPLET_SSH_KEYS=<fingerprint>   # doctl compute ssh-key list
mise run droplet:create
```

Defaults to `s-2vcpu-4gb` (4 GB / 2 vCPU / 80 GB, $24/mo) in `tor1`, which is
the closest region to Fly's `yyz`. Override with `DROPLET_SIZE`,
`DROPLET_REGION`, `DROPLET_IMAGE`.

Bootstrap takes several minutes after the droplet reports ready. Watch it:

```bash
ssh root@$IP 'tail -f /var/log/gritz-bootstrap.log'
```

It is finished when `/var/lib/cloud/gritz-bootstrap-done` exists. Confirm with:

```bash
ssh root@$IP 'dokku apps:list && dokku postgres:info gritz-db'
```

Consider enabling droplet backups (`doctl compute droplet-action enable-backups`,
+20% of the droplet price) now that the database shares the droplet's disk.

## 2. Point DNS at the droplet

Create an A record for the domain to the droplet's public IP, and wait for it to
resolve. This has to happen before step 5 - Let's Encrypt validates over HTTP
against the live name, which is why cloud-init installs the plugin but does not
issue a certificate.

```bash
ssh root@$IP 'dokku domains:set gritz <domain>'
```

## 3. Load the secrets

`dokku postgres:link` in step 1 already set `GRITZ_DATABASE_URL` to the local
service, so the copy in `sops.env.yml` (which still points at
`xagent-db.flycast`) must be excluded or it will send the new host straight back
to the Fly database:

```bash
sops --output-type json -d sops.env.yml \
  | jq -r 'del(.GRITZ_DATABASE_URL) | to_entries[] | "\(.key)=\(.value | tostring | @base64)"' \
  | xargs ssh root@$IP dokku config:set --no-restart --encoded gritz
```

`--encoded` is what makes `GRITZ_GITHUB_APP_PRIVATE_KEY` survive: it is a
multi-line PEM that does not round-trip through a plain `KEY=value` argument.

Then fix up the values that still carry the old name:

```bash
ssh root@$IP 'dokku config:set --no-restart gritz \
  GRITZ_BASE_URL=https://<domain> OTEL_SERVICE_NAME=gritz'
```

`GRITZ_GITHUB_APP_SLUG` is left alone - it names a registered GitHub App, and
renaming that is a separate exercise with its own callback-URL churn.

## 4. Deploy the image

The server image is published to GHCR by the release workflow and is public, so
the droplet needs no registry credentials.

**The image must be a post-rename build.** Anything up to and including
`xagent-server:2.18.0` predates the rename and reads `XAGENT_*` exclusively
(`cli.EnvVars("XAGENT_DATABASE_URL")` at that tag), so against the `GRITZ_*`
config from step 3 it starts with an empty DSN and crashloops on
`failed to open database: invalid url`. Deploying an old tag means renaming all
21 config vars, then renaming them back at the next release.

```bash
ssh root@$IP 'dokku git:from-image gritz ghcr.io/icholy/gritz-server:3.0.0'
```

The server runs its own migrations on startup (`store.Open(dsn, true)` in
`internal/command/server.go:174`), so there is no separate migration step. That
is also why this step has to come *before* the data move, or be undone by it -
see step 6.

## 5. Enable TLS

```bash
ssh root@$IP 'dokku letsencrypt:set gritz email <email> && dokku letsencrypt:enable gritz && dokku letsencrypt:cron-job --add'
```

## 6. Move the data

The source is Fly's `xagent-db`: a single `flyio/postgres-flex:17.2` machine in
`yyz` on a 1 GB volume. It is small - 16 MB total, of which the app's tables are
about 8 MB, the largest being `events` (~10k rows), `tasks` (~1.5k) and
`task_links` (~1.9k).

Two things about the source shape the dump:

- The app connects as `postgres` with no database in the path, so its tables are
  in the default `postgres` database, in the `public` schema.
- Fly's replication manager lives in that *same* database, as a `repmgr` schema
  and a `repmgr` extension. Dumping the whole database would carry both into the
  droplet, where the extension does not exist and the restore fails. Hence
  `--schema=public`, which also drops the extension, since `plpgsql` is the only
  other one and it is built in.

Postgres 17 into the droplet's 18.4 is the supported direction, but the client
must be at least as new as the *server*, so use an 18 client rather than
whatever the local distro ships.

Run this from a machine authenticated to Fly - the database is only reachable
over the Fly private network. Stop the app first so nothing writes mid-dump.

First reset the target database. Step 4 already let the server create its
schema in `gritz_db`, and `pg_restore` into a database that already holds those
objects fails on every one of them:

```bash
ssh root@$IP 'dokku ps:stop gritz && dokku postgres:connect gritz-db -c \
  "drop schema public cascade; create schema public"'
```

```bash
export PGPASSWORD=$(sops --output-type json -d sops.env.yml \
  | jq -r '.GRITZ_DATABASE_URL | capture("://[^:]+:(?<p>[^@]+)@").p')

fly scale count 0 -a xagent
fly proxy 5432 -a xagent-db &

docker run --rm --network host -e PGPASSWORD postgres:18-alpine \
  pg_dump --no-owner --no-acl --schema=public -Fc \
  -h localhost -p 5432 -U postgres -d postgres >gritz.dump

scp gritz.dump root@$IP:/tmp/
ssh root@$IP 'dokku postgres:import gritz-db </tmp/gritz.dump && rm /tmp/gritz.dump'
ssh root@$IP 'dokku ps:start gritz'
```

`dokku postgres:import` restores into the service's own `gritz_db` database;
`--no-owner --no-acl` is what lets the roles differ between the two hosts.

Check the restore landed before cutting DNS over:

```bash
ssh root@$IP "dokku postgres:connect gritz-db -c \
  'select relname, n_live_tup from pg_stat_user_tables order by n_live_tup desc'"
```

`schema_migrations` (dbmate) and `goose_db_version` (a leftover from the goose
era) both come across; the server's startup migration reconciles from
`schema_migrations`.

## 7. Cut CI over

`.github/workflows/deploy.yml` has been deleted - it ran `flyctl deploy` against
an app that no longer exists, and would have failed the first release after the
rename. Releases currently build and publish images but deploy nothing, so this
step is a manual `git:from-image` until the host is proven.

Restoring automatic deploys means a workflow that runs:

```
ssh dokku@<host> git:from-image gritz ghcr.io/icholy/gritz-server:${TAG#v}
```

with a deploy key in Actions secrets, added on the host with
`dokku ssh-keys:add github-actions`.

Keep the Fly app stopped rather than destroyed until the new host has run for a
while - `fly scale count 1 -a xagent` is the rollback.

## Notes on the current Fly setup

- The Fly apps are still named `xagent` and `xagent-db`; the rename was never
  applied to them. `fly.toml` in this repo says `app = "gritz"`, so `mise run
  deploy`, `mise run logs` and the `flyctl deploy` in `deploy.yml` all currently
  target an app that does not exist. Nothing has hit it because no release has
  been cut since the rename.
- The Fly machine is 1 shared CPU / 2 GB. The droplet is 2 vCPU / 4 GB and also
  carries Postgres, so it is still an upgrade.
- Fly has 22 secrets to sops' 21. The extra one is `XAGENT_GIHTUB_CLIENT_SECRET`,
  a typo'd duplicate of `XAGENT_GITHUB_CLIENT_SECRET` with an identical digest.
  It is correctly absent from `sops.env.yml` and should not be carried over.
