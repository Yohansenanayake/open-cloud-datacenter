#!/bin/bash
# Usage: ./build.sh <build_date> [pg_versions] [os_version]
# Example: ./build.sh 20260615
#          ./build.sh 20260615 "15 16 17"
#          ./build.sh 20260615 "16 17 18" "24.04"
set -euo pipefail

BUILD_DATE="${1:?Usage: ./build.sh <build_date> [pg_versions] [os_version]}"
PG_VERSIONS="${2:-15 16 17}"
OS_VERSION="${3:-22.04}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
IMAGES_YAML="$SCRIPT_DIR/images.yaml"
META_DATA_TEMPLATE="$SCRIPT_DIR/http/meta-data"
USER_DATA_TEMPLATE="$SCRIPT_DIR/http/user-data"
# A private, unpredictable directory rather than a PID-based /tmp path
# (CWE-377): mktemp -d creates it atomically with 0700 perms, so another
# user on the build host can't pre-place a symlink at a guessable name.
# ssh-keygen still needs the destination file itself to not already exist
# (it prompts to overwrite otherwise), so the key file lives inside this
# dir rather than being the mktemp target directly.
KEY_DIR="$(mktemp -d)"
KEY_FILE="$KEY_DIR/packer_key"
# Rendered seed files (meta-data copied as-is, user-data with this run's
# SSH key substituted in) live under the same private per-run KEY_DIR
# rather than being written in place into the shared http/ directory —
# two build.sh invocations against the same checkout (e.g. building the
# 22.04 and 24.04 streams concurrently) would otherwise sed-edit the same
# http/user-data file, and whichever build's ISO-bake step reads it last
# wins, leaving the other build's VM with the wrong SSH key baked in and
# no way for Packer's communicator to authenticate against it.
SEED_DIR="$KEY_DIR/seed"
mkdir -p "$SEED_DIR"

# Install yq if not present
if ! command -v yq &>/dev/null; then
  echo "==> yq not found — installing..."
  YQ_VERSION="v4.44.3"
  # mikefarah/yq doesn't publish a plain per-binary checksum file — every
  # platform's hash is bundled into one "checksums" file, keyed by column
  # position against "checksums_hashes_order" (SHA-256 is the 18th
  # algorithm listed there, so column 19 counting the filename). Pinned
  # here after fetching that release's checksums file and confirming this
  # value against the actual downloaded binary. Update both when bumping
  # YQ_VERSION.
  YQ_SHA256="a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7"
  TMP_YQ="$KEY_DIR/yq"
  wget -q -O "$TMP_YQ" \
    "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64"
  echo "${YQ_SHA256}  ${TMP_YQ}" | sha256sum -c -
  sudo install -m 0755 "$TMP_YQ" /usr/local/bin/yq
  echo "==> yq $(yq --version) installed"
fi

# Validate OS version and read ISO details from images.yaml
OS_EOL=$(yq ".os_streams.\"${OS_VERSION}\".eol" "$IMAGES_YAML")
if [[ "$OS_EOL" == "null" || -z "$OS_EOL" ]]; then
  echo "ERROR: OS '${OS_VERSION}' not found in images.yaml — add it before building"
  exit 1
fi
TODAY=$(date +%Y-%m-%d)
if [[ "$TODAY" > "$OS_EOL" ]]; then
  echo "ERROR: Ubuntu ${OS_VERSION} reached EOL on ${OS_EOL} — do not build new images for this stream"
  exit 1
fi
echo "==> OS: Ubuntu ${OS_VERSION} (EOL: ${OS_EOL}) — OK"

ISO_URL=$(yq ".os_streams.\"${OS_VERSION}\".iso_url" "$IMAGES_YAML")
if [[ "$ISO_URL" == "null" || -z "$ISO_URL" ]]; then
  echo "ERROR: OS '${OS_VERSION}' has no iso_url in images.yaml"
  exit 1
fi
ISO_CHECKSUM=$(yq ".os_streams.\"${OS_VERSION}\".checksum_url" "$IMAGES_YAML")
if [[ "$ISO_CHECKSUM" == "null" || -z "$ISO_CHECKSUM" ]]; then
  echo "ERROR: OS '${OS_VERSION}' has no checksum_url in images.yaml — refusing to build without base-image integrity verification"
  exit 1
fi

# Validate PG versions from images.yaml
echo "==> Validating PG versions against EOL policy (today: $TODAY)"
for ver in $PG_VERSIONS; do
  eol=$(yq ".pg_versions.\"${ver}\".eol" "$IMAGES_YAML")
  if [[ "$eol" == "null" || -z "$eol" ]]; then
    echo "ERROR: PG $ver not found in images.yaml — add it before building"
    exit 1
  fi
  if [[ "$TODAY" > "$eol" ]]; then
    echo "ERROR: PG $ver reached EOL on $eol — remove it from PG_VERSIONS before building"
    exit 1
  fi
  echo "  PG $ver: EOL $eol — OK"
done

# Install Packer if not present
if ! command -v packer &>/dev/null; then
  echo "==> Packer not found — installing..."
  PACKER_VERSION="1.11.2"
  # sha256sum -c resolves the filename column in SHA256SUMS relative to the
  # current directory, and that column is the upstream release's exact
  # filename — so the local download must keep that same name (not a
  # shortened one) or verification will report it "missing" rather than
  # actually checking it.
  PACKER_ZIP_NAME="packer_${PACKER_VERSION}_linux_amd64.zip"
  TMP_ZIP="$KEY_DIR/$PACKER_ZIP_NAME"
  TMP_SUMS="$KEY_DIR/packer_${PACKER_VERSION}_SHA256SUMS"
  wget -q -O "$TMP_ZIP" \
    "https://releases.hashicorp.com/packer/${PACKER_VERSION}/${PACKER_ZIP_NAME}"
  wget -q -O "$TMP_SUMS" \
    "https://releases.hashicorp.com/packer/${PACKER_VERSION}/packer_${PACKER_VERSION}_SHA256SUMS"
  (cd "$KEY_DIR" && grep "${PACKER_ZIP_NAME}$" "packer_${PACKER_VERSION}_SHA256SUMS" | sha256sum -c -)
  sudo unzip -q "$TMP_ZIP" -d /usr/local/bin/
  echo "==> Packer $(packer version) installed"
fi

# Install QEMU if not present
if ! command -v qemu-system-x86_64 &>/dev/null; then
  echo "==> QEMU not found — installing..."
  sudo apt-get update -y -qq
  sudo apt-get install -y -qq qemu-system-x86 qemu-utils ovmf
fi

cleanup() {
  rm -rf "$KEY_DIR"
}
trap cleanup EXIT

ssh-keygen -t ed25519 -f "$KEY_FILE" -N "" -C "packer-build" -q

cp "$META_DATA_TEMPLATE" "$SEED_DIR/meta-data"
sed "s|PACKER_SSH_PUBLIC_KEY_PLACEHOLDER|$(cat -- "${KEY_FILE}.pub")|g" \
  "$USER_DATA_TEMPLATE" > "$SEED_DIR/user-data"

export PACKER_SSH_PRIVATE_KEY_FILE="$KEY_FILE"

cd "$SCRIPT_DIR"
packer init ubuntu-postgres.pkr.hcl

OS_SHORT="${OS_VERSION/./}"
packer build \
  -var "build_date=${BUILD_DATE}" \
  -var "pg_versions=${PG_VERSIONS}" \
  -var "os_version=${OS_VERSION}" \
  -var "iso_url=${ISO_URL}" \
  -var "iso_checksum=${ISO_CHECKSUM}" \
  -var "seed_dir=${SEED_DIR}" \
  ubuntu-postgres.pkr.hcl

echo "Built: output-ubuntu-${OS_SHORT}-postgres-v${BUILD_DATE}/ubuntu-${OS_SHORT}-postgres-v${BUILD_DATE}.qcow2"
