<img src="assets/icon.svg" width="64" height="64" alt="">

# rollout

Deploy Docker Compose services to VPSs over SSH, from a Git repository.

Your infrastructure repo holds a `rollout.json` describing your servers and
which services run on them, plus one directory per service with its
`compose.yaml` and any files it needs. `rollout deploy` streams each service
directory to its servers over SSH and runs `docker compose up`. The same command
works on your machine and in GitHub Actions.

```
infra/
├── rollout.json
└── services/
    ├── whoami/
    │   └── compose.yaml
    └── api/
        ├── compose.yaml
        └── config/app.toml
```

## Install

```sh
brew install j4n-e4t/tap/rollout
# or
go install github.com/j4n-e4t/rollout/cmd/rollout@latest
```

## The environment file

```json
{
  "defaults": { "user": "deploy", "deployPath": "/opt/rollout", "stateDir": "/var/lib/rollout" },
  "servers": {
    "vps-1": { "host": "vps-1.tail1234.ts.net" },
    "vps-2": { "host": "203.0.113.11", "port": 2222, "hostKey": "ssh-ed25519 AAAA..." }
  },
  "services": {
    "whoami": { "servers": ["vps-1", "vps-2"] },
    "api": { "path": "services/api", "composeFile": "compose.yaml", "servers": ["vps-1"], "wait": true }
  }
}
```

| Field | Default | Notes |
| --- | --- | --- |
| `hooks.beforeDeploy` / `hooks.afterDeploy` | – | Remote shell hooks around each service deployment. Also supported under `services.<name>.hooks`. See [Deploy hooks](#deploy-hooks). |
| `output.showUnconfigured` | `false` | Include services not assigned to a server in the config in `ls` (including `--wide` and `--json`) and the dashboard. |
| `output.plain` | `false` | Disable colors and icons in rollout output, including prompts. |
| `output.color` / `output.icons` | automatic | Override colors or Nerd Font icons separately. Automatic styling requires a terminal outside CI with `TERM` other than `dumb`. |
| `defaults.user` / `port` | – | Apply to every server unless overridden. Without a user or port, ssh and `~/.ssh/config` decide. |
| `deployPath` (defaults or server) | `/opt/rollout` | Where service files go: `<deployPath>/<service>`. |
| `stateDir` (defaults or server) | `<deployPath>/.rollout` | rollout's own data: deploy records, history, locks, staging and rollback copies. Must not be a visible directory inside `deployPath`. |
| `servers.<name>.host` | required | Anything `ssh` accepts: IP, DNS name, Tailscale name, or a `~/.ssh/config` alias. |
| `servers.<name>.hostKey` | – | Pins the host key. Without it, ssh's normal `known_hosts` checking applies. |
| `services.<name>.path` | `services/<name>` | Relative to `rollout.json`. |
| `services.<name>.composeFile` | `compose.yaml` | |
| `services.<name>.servers` | required | The servers the service runs on. |
| `services.<name>.wait` | `true` | Run `compose up --wait` so failing or unhealthy containers fail the deploy. |
| `services.<name>.preserve` | `[]` | Top-level server entries to leave untouched during deployment and rollback; excluded from rollback copies. Must not appear in the service bundle. |

Server and service names must be lowercase (`a-z0-9_-`). The service name is used
as the Compose project name.

## CLI appearance

Interactive terminal output uses colors and Nerd Font icons for stacks,
servers, service states and deploy actions. `ls` and `history` detect the host
OS during their existing SSH requests. Linux hosts use the distribution icon
from the `ID` in `/etc/os-release` (or `/usr/lib/os-release`), such as Ubuntu,
Debian, Alpine or Fedora, falling back to the generic Linux icon for unknown
distributions. Other operating systems use Apple, FreeBSD or Windows icons;
unknown or unreachable hosts use a generic server icon. Use a
[Nerd Font](https://www.nerdfonts.com/) in your terminal to display the icons.

For plain output, add this top-level setting to `rollout.json`:

```json
"output": { "plain": true }
```

You can also run `rollout --plain ls` for a single invocation. Plain mode
wins over the individual settings. To keep colors without requiring a Nerd
Font, use `"output": { "icons": false }`. Set `color` or `icons` to `true`
to explicitly enable either in redirected output; by default pipes, CI and
`TERM=dumb` use plain output. A nonempty `NO_COLOR` environment variable
disables colors. JSON output always remains unstyled. `logs` also disables
Compose colors when colors are off; output from commands run with `exec`
is controlled by those commands.

## Usage

```sh
rollout validate                # check rollout.json
rollout deploy                  # pick services interactively
rollout deploy web api          # deploy them to their configured servers
rollout deploy --all            # deploy whatever changed
rollout deploy --all --dry-run  # show what would change
rollout deploy web -r v1.4 -f   # name the release, redeploy even if unchanged
rollout deploy api -m "Raise worker concurrency to 8"   # describe the deploy
rollout ls                      # live state and release of every service
rollout ls web -w               # one service, with its full deploy record
rollout ls --json               # machine-readable
rollout history [web] [-n 20]   # releases per server: version, commit, when, who
rollout secrets                 # secrets referenced in .env files, and which are set
rollout import --all -n         # adopt stacks that already run on the servers
rollout logs web [nginx] -f     # docker compose logs of a service
rollout exec web nginx          # shell in a container (or: -- <command>)
rollout dashboard               # local web dashboard, opens your browser
rollout init renovate           # write an example Renovate config
```

Which service runs on which server is defined only in `rollout.json`; there is
no command-line override.

`rollout ls` connects to each server and shows, per service and server, its
state (`running`, `starting`, `unhealthy`, `degraded`, `stopped`,
`not deployed`, `unreachable`), how many containers are up, the live release
and when it was deployed. Images are shown only in `--wide` container details
and `--json` output.
Services found on a server that the config does not assign to it are hidden
by default. Set `"output": { "showUnconfigured": true }` in `rollout.json` to
include them, marked `(not in config)`, in text, `--wide` and `--json` output.
It exits non-zero if any configured
service isn't running, so it works as a post-deploy check in CI.

```
SERVICE  SERVER  STATE                    CONTAINERS  RELEASE  DEPLOYED
api      vps-1   not deployed             -           -        -
web      vps-1   running                  1/1         v1.4     5m ago
web      vps-2   unreachable              -           -        -
```

Servers are deployed in parallel. Services on the same server are deployed one
after another, in the order given.

### Deploy records and incremental deploys

Every successful deploy writes a record to the server, in
`<stateDir>/<service>.json`, and appends it to
`<service>.history.jsonl` (last 200 deploys). Because it lives on the server,
every deploy user, whether a laptop or CI, sees the same state.

```json
{
  "version": "v1.4",
  "message": "Raise worker concurrency to 8",
  "commit": "3fa46ac32a7ac705a9aff70a6fc2cf095e2a8cef",
  "dirty": false,
  "hash": "sha256:715f6c2d…",
  "secrets": "pbkdf2-sha256:3ade6d23…",
  "deployedAt": "2026-10-29T17:10:02Z",
  "deployedBy": "julian@mac",
  "source": "https://github.com/acme/infra/actions/runs/123",
  "rollout": "v0.3.0",
  "images": [{ "ContainerName": "web-nginx-1", "Repository": "nginx", "Tag": "alpine", "ID": "sha256:df22…" }]
}
```

- `version`: the optional name given with `deploy --release`.
- `message`: what the deploy is for, like a commit message: `deploy -m "…"`,
  else the subject line of the current Git commit. It's shown in `history`,
  `ls -w`, the deploy plan and the dashboard.
- `commit` / `dirty`: Git `HEAD` of the environment repo, and whether the service
  directory had uncommitted changes.
- `hash`: covers every shipped file (path, content, executable bit, symlink
  target) and the compose file name. Timestamps and owners are ignored, so the
  same checkout hashes the same everywhere.
- `secrets`: a salted, slow digest (PBKDF2) of the secret values filled into
  the service's `.env` files (see [Secrets](#secrets)). It changes when a
  secret does, without revealing it. Absent when the service uses no secrets.
- `source`: link to the GitHub Actions run, when deployed from CI.
- `images`: the exact image IDs the containers run.

Before deploying, rollout reads the records and compares hashes. A service is
**skipped** when its hash and secrets digest match the server's record *and*
it is running there. New, changed, or stopped services, and servers whose state can't be read,
are deployed. `--force` deploys everything selected. `--pull always` also bypasses
file-based skipping so mutable image tags are refreshed. Status checks the live
Compose configuration for missing services and replicas before calling a service
running; inactive profiles are optional and successful one-shot jobs count as complete.

`rollout history [service...]` lists the releases on each server, newest first.
The live one (from `<service>.json`) is marked `●`, and earlier ones come from
the history file. Each row shows the release name, commit, time, who deployed
it, whether the files changed since the previous release (`changed`, `same`,
or `secrets` when only secret values changed), and the deploy message. Use `-n` to show
more, `-s` to pick one server, and `--json` for scripts:

```
web on vps-1
     RELEASE  COMMIT   DEPLOYED                     BY          FILES         MESSAGE
  ●  v3       9be6a12  2026-10-29 19:55  (5m ago)   julian@mac  changed       Add extra.txt to web
     v2       3fa46ac  2026-10-29 18:09  (2h ago)   julian@mac  same          Redeploy after host reboot
     v1       b146f33  2026-10-29 18:08  (2h ago)   julian@mac  first deploy  Initial web setup
``` So `deploy --all` on every
push to main only touches what changed.

### Secrets

Secrets stay out of the repository. Commit each service's `.env` (or any
`*.env` file, e.g. for `env_file:`) with **references** instead of values:

```sh
# services/api/.env
DB_PASSWORD=${DB_PASSWORD}
DATABASE_URL=postgres://app:${DB_PASSWORD}@db:5432/app
LOG_LEVEL=${LOG_LEVEL:-info}   # default when unset or empty
PRICE=costs $$5                # $$ is a literal $
```

Many `.gitignore` templates exclude `.env`. Add an exception such as
`!services/*/.env` so the reference files are committed.

`rollout deploy` fills the references in on the deploying machine and ships
the result; nothing with values in it is written locally. Values come from:

1. environment variables (`DB_PASSWORD=… rollout deploy api` on a laptop), then
2. `ROLLOUT_SECRETS`, a JSON object. The GitHub Action sets it from its
   `secrets` input, so `secrets: ${{ toJSON(secrets) }}` makes every
   repository secret available. Add one under *Settings → Secrets and
   variables → Actions* with the same name as the reference.

Supported forms are `${NAME}` and `$NAME` (required), `${NAME:-default}`,
`${NAME-default}` and `$$`. Single-quoted values are left alone, as in Docker
Compose. Each line with a reference is rewritten as a double-quoted value,
escaped so Compose reads back exactly the secret, quotes, `$`, backslashes and
line breaks included. Other lines are shipped unchanged.

If a required secret is not set, the deploy stops before touching any server
and names what is missing. `rollout secrets` lists every reference, where it
is, and whether it is set, `set`, `default` or `MISSING`, which is handy when
setting up repository secrets:

```
SERVICE  SECRET       STATUS   WHERE
api      DB_PASSWORD  MISSING  .env:2
api      LOG_LEVEL    default  .env:4
```

On the server, rendered env files are written with mode `0600`. Changing a
secret's value redeploys the services that use it (the plan says
`secrets changed`), even when no file changed. The dashboard compares files
only, since it has no access to the secrets.

### Importing existing deployments

Stacks started by hand, for example with `docker compose up` in
`/root/paperless-ngx`, can be taken over. Name the service like the compose
project (usually its directory name) and assign it to the server:

```sh
rollout ls                      # already shows the running project
rollout import paperless-ngx -n # what would be imported
rollout import paperless-ngx    # write an "imported" deploy record
rollout deploy paperless-ngx    # take it over
```

`import` writes a deploy record marked `imported`, with the directory the
project runs from and its current images. Containers and files are not
touched. From then on `ls`, `history` and the dashboard treat it as
deployed, and `logs` and `exec` work against the old directory.

Importing is optional: a deploy plan shows a running project that rollout has
no record of as `existing`, and deploying takes it over the same way. If that
deploy fails, the rollback puts the original project back up from its old
directory; it is never stopped to clean up.

The next deploy sees the service as changed and ships the repo's files to
`<deployPath>/<service>`, running `compose up` under the same project name.
Named volumes and networks are reused. Containers whose configuration
didn't change keep running untouched. The old directory is left as it is.

Before the first deploy, point relative bind mounts (`./data`) at their
current absolute location (`/root/paperless-ngx/data`). Otherwise the
new deploy directory would start empty. The data stays where it is, so
backup scripts keep working.

### SSH

rollout runs your system `ssh`, so everything in `~/.ssh/config`, ssh-agent,
`ProxyJump` and [Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh)
works as it does in your shell.

- `-i, --identity <file>` is passed to ssh as `-i`.
- `ROLLOUT_SSH_KEY` holds private key contents, for CI secrets. rollout writes it
  to a private temp file and passes it with `-i`.
- `hostKey` in the config pins a server's key (get it with
  `ssh-keyscan -t ed25519 <host>`). Otherwise ssh's normal `known_hosts`
  checking applies. `--insecure-ignore-host-key` turns checking off.
- Outside an interactive terminal (or when `CI` is set), ssh runs with
  `BatchMode=yes`, so it fails instead of waiting for a prompt.

### Dashboard

`rollout dashboard` (or `rollout ui`) serves a read-only page on
`http://127.0.0.1:7878` and opens it (`--addr` to change, `--no-open` to skip
the browser). It's one embedded HTML file, no build step or framework.
Open the session URL printed by the command: each dashboard process has its own
random API token, and requests must use that URL's host and port. The page removes
the token from the address bar after loading and keeps it for that browser tab.

- **Breadcrumb navigation**: use the selectors at the top left to choose a
  server, then a stack. The server overview lists its stacks; selecting a stack
  shows its Compose services. Each service groups its individual container
  cards, so replicas retain their own health, image, restart count and uptime.
- **Container resources**: volumes, bind mounts and temporary storage appear
  below their own container, with read-only mounts marked. Published ports can
  open the host endpoint; loopback-only ports remain labels. Networks show their
  runtime names, IPs and aliases.
- **Service links**: follow a declared dependency or shared network to another
  service. Cross-stack links work for services on shared networks on the same
  server. Shared network membership indicates access, not measured traffic.
- **Health and sync**: stacks report health and whether the deployed bundle
  matches your local files. Services that are not assigned to a server in the
  config are hidden unless `output.showUnconfigured` is enabled.
- **Details panel**: select a container for its runtime details, or open stack
  details for the live release, configuration and copyable CLI commands.
  *Deployments*, *Compose* and *Files* retain release history and source views.

Desired state comes from your local compose files; live state comes over SSH
(`docker ps`/`docker inspect`; container environment variables are never read).
It auto-refreshes every 30 s while the tab is visible. Navigation is shareable:
`#/<server>/<stack>` selects a stack, and
`#/<server>/<stack>/<tab>/<service>/<container>` opens a detail panel. Browser
back/forward restores the selected server and stack. The dashboard supports
light and dark system themes and narrow screens without external assets.

Dashboard browser tests use intercepted API responses, without SSH or a running
server. To run them, install the test dependencies and WebKit once:

```sh
cd internal/dashboard
npm install
npx playwright install webkit
npm test
```

### Logs and exec

`rollout logs <service> [container...]` runs `docker compose logs` in the
service's directory (`-f` to follow, `-n` lines, `-t` timestamps). A service on
several servers streams all of them, prefixed with the server name, unless
`-s <server>` picks one.

`rollout exec <service> <container> [-- command...]` runs `docker compose
exec`; without a command it opens a shell (bash, else sh). For a service on
several servers, pass `-s` or pick one interactively. The remote exit code is
passed through.

### Renovate

`rollout init renovate` writes `renovate.json5` next to `rollout.json`. It pins
images to digests (`nginx:1.27@sha256:…`), so a deploy runs exactly the
reviewed image; it opens separate branches per service directory, groups
non-major updates, and holds major updates for approval on the Dependency
Dashboard. Automerging digest and patch updates is in the file, commented
out. Enable the Renovate GitHub app on the repository to get PRs.

## Deploy hooks

Define remote POSIX shell commands in `rollout.json`, either for all services
or for a particular service:

```json
{
  "hooks": {
    "beforeDeploy": "echo Preparing $ROLLOUT_SERVICE on $ROLLOUT_SERVER",
    "afterDeploy": "echo Deployed $ROLLOUT_SERVICE release $ROLLOUT_RELEASE"
  },
  "servers": {
    "vps-1": { "host": "203.0.113.10" }
  },
  "services": {
    "web": {
      "servers": ["vps-1"],
      "hooks": {
        "beforeDeploy": "sh scripts/backup.sh",
        "afterDeploy": "docker compose exec -T app ./migrate"
      }
    }
  }
}
```

Hooks run as the SSH user, once per service/server pair that actually deploys,
inside the deployment lock. Servers can deploy in parallel. Dry runs, skipped
unchanged services, and commands such as `ls`, `history` and `import` do not
run hooks. Changing only a hook does not trigger a deployment; use `--force`
when you want to redeploy an otherwise unchanged service.

The order is global `beforeDeploy`, service `beforeDeploy`, deployment,
service `afterDeploy`, global `afterDeploy`. A failing hook stops that sequence.
Each command runs in its own `sh -eu` process with no stdin; output appears
with the usual server/service prefix. Shell quoting, pipes and multiline
commands work. A hook's `cd`, variables and `exit` cannot alter the parent
shell.

Before hooks run after Compose validation and image pulling, before the live
files change. Their working directory is the **staged new bundle**, so scripts
included in the new service directory are available even on a first deploy.
A before-hook failure aborts that service deployment. After hooks run in the
**live service directory**, after startup, the release record commit and cleanup.
An after-hook failure returns an error and leaves the committed release running.
After hooks do not run on deployment failure or rollback. Hook side effects,
such as database changes, are not undone by rollout's file recovery.

Hooks receive these environment variables:

| Variable | Value |
| --- | --- |
| `ROLLOUT_SERVICE` | Service name. |
| `ROLLOUT_SERVER` | Server name from `rollout.json`. |
| `ROLLOUT_DEPLOY_PATH` | Absolute path of the live service directory. |
| `ROLLOUT_STATE_DIR` | Absolute path of rollout's state directory. |
| `ROLLOUT_RELEASE` | New release label as shown by `ls` (version, commit or bundle hash). |

The remote environment is inherited; service `.env` files are not sourced
into the hook shell. Use `docker compose exec -T` for commands inside a container.

## What happens on the server

For each service, in a single SSH session:

1. Takes a lock (`flock`) so two deploys of the same service can't overlap.
2. Unpacks the service directory to `<stateDir>/work/<name>.incoming`.
3. Validates it with `docker compose config`.
4. Pulls the images (`docker compose pull`, `--pull missing` by default or
   `--pull always`) while the old release keeps running, so the switch only has
   to recreate containers. A pull failure aborts here, before anything changes.
5. Runs global and service `beforeDeploy` hooks, then backs up the live directory to `<stateDir>/work/<name>.prev`, then syncs the new files into
   `<deployPath>/<name>` in place, so bind mounts of running containers stay valid.
6. Runs `docker compose -p <name> up -d --remove-orphans --wait`, then writes
   the deploy record.
7. If file sync, startup, or live-record persistence fails, keeps the new release
   in `<stateDir>/work/<name>.failed`, restores the previous files and record, and starts them
   again. A failed first deploy stops the new containers and removes the live
   directory. HUP, INT and TERM also trigger recovery.

`flock` is required on the server; deployment stops before extraction if it is
missing. The backup is retained until the new live record is committed. A history
append failure after that commit is reported as a warning. Service and global
`afterDeploy` hooks run after cleanup, while the lock is still held.

If recovery itself fails or the process is killed without a chance to recover,
`<stateDir>/<name>.pending` remains. Status reports the service as
incomplete, and further deploys stop before replacing recovery data. Inspect
`<stateDir>/work/<name>.prev`, `<name>.record.prev`, and `<name>.failed`; restore the files and
record and verify the containers before removing the pending marker and retrying.
Recovery restores configuration files; pin image digests to make the previous
image reproducible as well.

Files removed from the repo are removed on the server. Keep persistent data in
named volumes or absolute host paths, not in relative bind mounts like `./data`.

The SSH user needs write access to `deployPath` and `stateDir` and permission to use Docker
(for example, membership in the `docker` group).

## GitHub Actions

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: j4n-e4t/rollout@main
        with:
          ssh-key: ${{ secrets.DEPLOY_SSH_KEY }}
          secrets: ${{ toJSON(secrets) }}   # for ${NAME} references in .env files
          args: deploy --all   # only changed services are deployed
```

The action requires Python 3 on the runner (included on GitHub-hosted Ubuntu
runners). Its `args` input supports POSIX-style quoting, for example
`deploy --all -m "Ship worker"`. Shell substitutions and wildcard expansion are
not performed.

With Tailscale SSH there's no key to manage: join the tailnet with
[`tailscale/github-action`](https://github.com/tailscale/github-action) before
running rollout, and leave out `ssh-key`.

See [`examples/`](examples) for a complete infrastructure repo layout and
workflow. Because image tags live in plain `compose.yaml` files, Renovate's
`docker-compose` manager can open PRs that bump them, and merging such a PR
deploys it.

For existing projects with data inside their working directory, set `deployPath`
to the original parent directory and list every persistent top-level entry in
`services.<name>.preserve`, for example `["data", "media", "export", "consume"]`.
These entries are never copied, replaced or deleted during deployment or rollback.
Keep the original `composeFile` name when backup scripts use Compose auto-discovery.
