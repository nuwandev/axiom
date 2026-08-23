# Getting Started

This is the fast path from "nothing installed" to "one action successfully
triggered." For the complete reference — permissions, systemd hardening,
SELinux, upgrade, rollback, uninstall, troubleshooting — see
[`INSTALL.md`](INSTALL.md). For the config format in depth, see
[`configuration.md`](configuration.md). For how action scripts work, see
[`actions.md`](actions.md).

Target platform: a RHEL-family Linux host with systemd (RHEL, Rocky Linux,
AlmaLinux, CentOS Stream).

## 1. Obtain a release

Download a release binary for your architecture from
[Releases](https://github.com/nuwandev/axiom/releases), or build from
source:

```bash
git clone https://github.com/nuwandev/axiom.git
cd axiom
go build -o axiom ./cmd/axiom
```

Verify the checksum of a downloaded release against the published
`SHA256SUMS` file before using it.

## 2. Install Axiom

```bash
sudo BIN_SRC=./axiom ./scripts/install.sh
```

This creates the dedicated `axiom` service account, the directory layout
under `/etc/axiom`, `/opt/axiom/actions`, `/var/log/axiom`, `/var/lib/axiom`,
installs the binary to `/usr/local/bin/axiom`, and installs the systemd
unit. It does **not** generate certificates, write a config if one already
exists, or start the service — safe to re-run any time.

## 3. Configure certificates

Axiom never generates certificate material — that's your PKI process. You
need an internal CA, one server certificate for this agent, and one client
certificate per system that will call it:

```bash
sudo install -o root -g axiom -m 0640 ca.crt server.crt /etc/axiom/certs/
sudo install -o root -g axiom -m 0640 server.key /etc/axiom/certs/
```

See [`INSTALL.md` §5](INSTALL.md#5-certificates) for exactly what Axiom
validates here and why the permissions matter.

## 4. Configure actions

Copy the annotated example and edit it for your environment:

```bash
sudo install -o root -g axiom -m 0640 configs/example.yaml /etc/axiom/config.yaml
sudo vi /etc/axiom/config.yaml
```

Place each action's script under `/opt/axiom/actions/`, owned `root:axiom`,
mode `0750`:

```bash
sudo install -o root -g axiom -m 0750 my-deploy.sh /opt/axiom/actions/
```

See [`configuration.md`](configuration.md) and [`actions.md`](actions.md)
for the full model.

## 5. Start the service

```bash
sudo systemctl enable --now axiom
sudo systemctl status axiom
```

A clean start logs one structured line with the listen address. Any
config or security problem exits immediately with a specific error —
check `journalctl -u axiom -n 50` first if it doesn't come up.

## 6. Verify health

```bash
curl --cacert ca.crt --cert client.crt --key client.key \
  https://<host>:<port>/health
```

Expect `{"status":"ok","agent":"<agent.id>","version":"..."}`.

## 7. Trigger an action

```bash
curl --cacert ca.crt --cert client.crt --key client.key \
  -X POST https://<host>:<port>/v1/actions/<action-name> \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{}}'
# {"job_id":"01J...","status":"queued"}

curl --cacert ca.crt --cert client.crt --key client.key \
  https://<host>:<port>/v1/jobs/01J...
```

That's the full loop. From here: [`configuration.md`](configuration.md) for
every field, [`actions.md`](actions.md) for how to write action scripts
safely, and [`operations`](INSTALL.md) for running this in production.
