#!/bin/bash
set -euo pipefail

# This script initializes a filesystem, never SQLite or Redis. The executor
# will have its own explicit store-initialization contract.
STATE_DEVICE=/dev/disk/by-id/virtio-dbaas-state
STATE_MOUNT=/var/lib/dbaas-state
ATTEMPT_MARKER=/var/lib/dbaas/guest-state-initialization-attempted

fail() { echo "DBaaS guest state: $*" >&2; return 1; }
is_block_device() { test -b "$STATE_DEVICE"; }
filesystem_type() { blkid -p -s TYPE -o value "$STATE_DEVICE"; }

mount_state() {
    if ! mountpoint -q "$STATE_MOUNT"; then
        install -d -o root -g root -m 0711 "$STATE_MOUNT"
        mount -t ext4 -o nodev,nosuid,noexec "$STATE_DEVICE" "$STATE_MOUNT"
    fi
    local actual
    actual=$(findmnt -rn -M "$STATE_MOUNT" -o SOURCE)
    [ "$(readlink -f "$actual")" = "$(readlink -f "$STATE_DEVICE")" ] || fail 'unexpected mounted device'
    [ "$(findmnt -rn -M "$STATE_MOUNT" -o FSTYPE)" = ext4 ] || fail 'unexpected mounted filesystem'
    case ",$(findmnt -rn -M "$STATE_MOUNT" -o OPTIONS)," in
        *,rw,*) ;;
        *) fail 'state filesystem is not writable';;
    esac
}

prepare_state() {
    local mode=$1 fs_type signatures rc=0 formatted=false
    [ -n "$INSTANCE_UID" ] && [ -n "$STATE_PVC_UID" ] || fail 'missing instance or PVC identity'
    is_block_device || fail 'required state disk is missing'
    fs_type=$(filesystem_type) || rc=$?
    if [ "$rc" = 2 ] && [ -z "$fs_type" ]; then
        [ "$mode" = initialize ] && [ "$INITIALIZE_STATE" = true ] || fail 'empty established disk; recovery required'
        [ ! -e "$ATTEMPT_MARKER" ] || fail 'initialization already attempted; recovery required'
        # Do not erase partition tables, other filesystem signatures or an
        # ambiguous blkid probe. There is intentionally no mkfs force flag.
        signatures=$(wipefs --no-act --noheadings --output TYPE "$STATE_DEVICE") || fail 'cannot inspect disk signatures'
        [ -z "$signatures" ] || fail 'disk contains an unrecognized signature'
        install -d -o root -g root -m 0700 "$(dirname "$ATTEMPT_MARKER")"
        (set -o noclobber; printf '%s\n' "$STATE_PVC_UID" > "$ATTEMPT_MARKER")
        sync -f "$ATTEMPT_MARKER"
        sync -f "$(dirname "$ATTEMPT_MARKER")"
        mkfs.ext4 -q -L dbaas-state "$STATE_DEVICE"
        formatted=true
    elif [ "$rc" != 0 ] || [ "$fs_type" != ext4 ]; then
        fail 'state disk is unreadable or has an unexpected filesystem'
    fi
    mount_state
    local identity="$STATE_MOUNT/volume-identity" expected
    expected=$(printf '%s\n%s' "$INSTANCE_UID" "$STATE_PVC_UID")
    if [ "$formatted" = true ]; then
        # A crash before this durable identity write blocks automatic adoption.
        umask 077
        printf '%s\n' "$expected" > "$STATE_MOUNT/.volume-identity.tmp"
        sync -f "$STATE_MOUNT/.volume-identity.tmp"
        mv "$STATE_MOUNT/.volume-identity.tmp" "$identity"
        sync -f "$STATE_MOUNT"
    fi
    [ -f "$identity" ] && [ ! -L "$identity" ] || fail 'volume identity is missing; recovery required'
    [ "$(cat "$identity")" = "$expected" ] || fail 'volume belongs to a different instance or PVC'
    [ "$(stat -c '%u:%g:%a' "$identity")" = '0:0:600' ] || fail 'invalid volume identity permissions'
}

main() {
    [ "$(id -u)" = 0 ] || fail 'root is required'
    case "${1:-}" in initialize|mount) ;; *) fail 'expected initialize or mount';; esac
    source /etc/dbaas/guest-state.env
    udevadm settle --timeout=30
    prepare_state "$1"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
