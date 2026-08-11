# DBaaS guest-tools Debian package

This package installs the two restricted guest executables used by the DBaaS
serial-console command path:

- `/usr/lib/dbaas/dbaas-console`, the `dbaas-ops` login shell;
- `/usr/lib/dbaas/dbaas-backupctl`, the privileged fixed-operation helper;
- `/etc/sudoers.d/dbaas-guest-tools`, the single helper authorization; and
- `/usr/share/dbaas-guest-tools/version`, package version metadata.

It intentionally contains no account password, DBInstance UID, repository
configuration, object-storage credential, or tenant-specific data. Cloud-init
creates `dbaas-ops`, sets its per-instance password and shell, and writes the
DBInstance UID to `/etc/dbaas/instance-uid`.

## Build

The package currently targets the Milestone 1 Ubuntu `amd64` image.

```bash
make guest-tools GUEST_TOOLS_VERSION=0.1.0
```

This produces:

```text
dist/guest-tools/dbaas-guest-tools_0.1.0_amd64.deb
dist/guest-tools/dbaas-guest-tools_0.1.0_amd64.deb.sha256
```

Before installing on a disposable development VM, publish both files as assets
on the same GitHub Release. On the VM, download both files and verify the
checksum before installation:

```bash
sha256sum --check dbaas-guest-tools_0.1.0_amd64.deb.sha256
sudo apt install ./dbaas-guest-tools_0.1.0_amd64.deb
/usr/lib/dbaas/dbaas-console --version
/usr/lib/dbaas/dbaas-backupctl --version
sudo visudo -c
```

For a local wrapper smoke test after cloud-init has configured `dbaas-ops` and
`/etc/dbaas/instance-uid`, form a v1 `probe` request with the configured UID and
pipe its unpadded base64url line into the wrapper while running as `dbaas-ops`.
The output must contain `DBAAS-CONSOLE-READY-V1` followed by one encoded
`Succeeded` response. The live serial-console flow is added and validated in
PR 2.

```bash
instance_uid=$(sudo tr -d '\r\n' </etc/dbaas/instance-uid)
request=$(printf '{"protocolVersion":1,"requestID":"local-probe-1","instanceUID":"%s","operation":"probe","payload":{}}' "$instance_uid" \
  | basenc --base64url -w0 | tr -d '=')
printf '%s\n' "$request" | sudo -u dbaas-ops /usr/lib/dbaas/dbaas-console
```
