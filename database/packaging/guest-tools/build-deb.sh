#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 VERSION OUTPUT_DIR DBAAS_CONSOLE DBAAS_BACKUPCTL" >&2
  exit 2
fi

version=$1
output_dir=$2
console_binary=$3
backupctl_binary=$4

if [[ ! $version =~ ^[0-9][0-9A-Za-z.+:~-]*$ ]]; then
  echo "invalid Debian package version" >&2
  exit 2
fi
if [[ ! -x $console_binary || ! -x $backupctl_binary ]]; then
  echo "guest-tool binaries must exist and be executable" >&2
  exit 2
fi

for tool in dpkg-deb sha256sum visudo; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required packaging tool is unavailable: $tool" >&2
    exit 2
  fi
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
staging_dir=$(mktemp -d)
trap 'rm -rf -- "$staging_dir"' EXIT
chmod 0755 "$staging_dir"

install -d -m 0755 \
  "$staging_dir/DEBIAN" \
  "$staging_dir/etc/dbaas" \
  "$staging_dir/etc/sudoers.d" \
  "$staging_dir/usr/lib/dbaas" \
  "$staging_dir/usr/share/dbaas-guest-tools"
install -m 0755 "$console_binary" "$staging_dir/usr/lib/dbaas/dbaas-console"
install -m 0755 "$backupctl_binary" "$staging_dir/usr/lib/dbaas/dbaas-backupctl"
install -m 0440 "$script_dir/etc/sudoers.d/dbaas-guest-tools" "$staging_dir/etc/sudoers.d/dbaas-guest-tools"
printf '%s\n' "$version" >"$staging_dir/usr/share/dbaas-guest-tools/version"
chmod 0444 "$staging_dir/usr/share/dbaas-guest-tools/version"

sed "s/@VERSION@/$version/g" "$script_dir/debian/control.in" >"$staging_dir/DEBIAN/control"
chmod 0644 "$staging_dir/DEBIAN/control"
visudo -c -f "$staging_dir/etc/sudoers.d/dbaas-guest-tools" >/dev/null

mkdir -p "$output_dir"
package_name="dbaas-guest-tools_${version}_amd64.deb"
package_path="$output_dir/$package_name"
dpkg-deb --build --root-owner-group "$staging_dir" "$package_path" >/dev/null
(
  cd "$output_dir"
  sha256sum "$package_name" >"$package_name.sha256"
)

printf '%s\n' "$package_path"
printf '%s\n' "$package_path.sha256"
