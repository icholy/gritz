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

Note that **`ghcr.io/icholy/gritz-server` does not exist yet**. The rename landed
after the last release, so the newest published server image is still
`ghcr.io/icholy/xagent-server:2.18.0`. Either deploy that tag for the cutover, or
cut a release first and deploy the `gritz-server` tag it produces.

```bash
ssh root@$IP 'dokku git:from-image gritz ghcr.io/icholy/xagent-server:2.18.0'
```

The server runs its own migrations on startup (`store.Open(dsn, true)` in
`internal/command/server.go`), so there is no separate migration step.

## 5. Enable TLS

```bash
ssh root@$IP 'dokku letsencrypt:set gritz email <email> && dokku letsencrypt:enable gritz && dokku letsencrypt:cron-job --add'
```

## 6. Move the data

Run this from a machine authenticated to Fly, since the source is only reachable
over the Fly private network. Take the app down first so nothing writes during
the dump.

```bash
fly scale count 0 -a gritz
fly proxy 5432 -a xagent-db &
pg_dump --no-owner --no-acl -Fc "postgres://<user>:<pass>@localhost:5432/gritz" -f gritz.dump
scp gritz.dump root@$IP:/tmp/
ssh root@$IP 'dokku postgres:import gritz-db < /tmp/gritz.dump && rm /tmp/gritz.dump'
```

Use a `pg_dump` at least as new as the Fly server's version, or it will refuse
with a server-version mismatch. `docker run --rm postgres:18-alpine pg_dump ...`
is the easy way to get one.

## 7. Cut CI over

`.github/workflows/deploy.yml` still runs `flyctl deploy`. Replacing that step
with a Dokku deploy of the released tag is the last step, and is best done only
once a manual deploy has been verified end to end:

```
ssh dokku@<host> git:from-image gritz ghcr.io/icholy/gritz-server:${TAG#v}
```

That needs a deploy key in Actions secrets, added with
`ssh root@$IP 'dokku ssh-keys:add github-actions'`.

Keep the Fly app stopped rather than destroyed until the new host has run for a
while - `fly scale count 1 -a gritz` is the rollback.
