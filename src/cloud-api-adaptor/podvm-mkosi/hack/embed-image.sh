#!/usr/bin/env bash
# Appends a pre-populated ext4 partition to a podvm raw disk image.
# Usage: embed-image.sh <system.raw> <image-store-dir> <output.qcow2>
# Must be run as root (requires loop device + mount).

set -euo pipefail

RAW_IMAGE="${1:?Usage: embed-image.sh <system.raw> <image-store-dir> <output.qcow2>}"
IMAGE_STORE="${2:?}"
OUTPUT_QCOW2="${3:?}"

if [ ! -f "$RAW_IMAGE" ]; then
    echo "ERROR: Raw image not found: $RAW_IMAGE" >&2
    exit 1
fi

if [ ! -d "$IMAGE_STORE" ]; then
    echo "ERROR: Image store directory not found: $IMAGE_STORE" >&2
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root" >&2
    exit 1
fi

LOOP_DEV=""
MOUNT_POINT=""

cleanup() {
    local exit_code=$?
    if [ -n "$MOUNT_POINT" ] && mountpoint -q "$MOUNT_POINT" 2>/dev/null; then
        umount "$MOUNT_POINT" || true
    fi
    [ -n "$MOUNT_POINT" ] && rmdir "$MOUNT_POINT" 2>/dev/null || true
    [ -n "$LOOP_DEV" ] && losetup -d "$LOOP_DEV" 2>/dev/null || true
    # Clean up working copy on failure (success path already has rm -f)
    if [ $exit_code -ne 0 ]; then
        rm -f "$WORK_IMAGE" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Work on a copy so system.raw remains usable for the generic qcow2
WORK_IMAGE="${RAW_IMAGE%.raw}-embedded.raw"
echo "Copying $RAW_IMAGE -> $WORK_IMAGE ..."
cp "$RAW_IMAGE" "$WORK_IMAGE"

# Calculate partition size: image store size + 100% headroom (minimum 512 MB).
# CDH decompresses layer blobs into overlay/ on the same partition when creating
# the container rootfs, so it needs ~1x the image size of additional free space.
IMAGE_SIZE_MB=$(du -sm "$IMAGE_STORE" | cut -f1)
HEADROOM_MB=$(( IMAGE_SIZE_MB ))
HEADROOM_MB=$(( HEADROOM_MB < 512 ? 512 : HEADROOM_MB ))
PARTITION_SIZE_MB=$(( IMAGE_SIZE_MB + HEADROOM_MB ))

echo "Image store: ${IMAGE_SIZE_MB} MB, partition: ${PARTITION_SIZE_MB} MB"

# Extend the raw image file to make room for the new partition
CURRENT_BYTES=$(stat -c%s "$WORK_IMAGE")
NEW_BYTES=$(( CURRENT_BYTES + PARTITION_SIZE_MB * 1024 * 1024 ))
fallocate -l "$NEW_BYTES" "$WORK_IMAGE"

# Append a new GPT partition using all newly added space
echo "size=${PARTITION_SIZE_MB}MiB, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=embedded_image" \
    | sfdisk --append "$WORK_IMAGE"

# Attach as loop device with partition scanning
LOOP_DEV=$(losetup -f --show -P "$WORK_IMAGE")
echo "Attached as $LOOP_DEV"

# Give the kernel time to create the partition device node
udevadm settle || sleep 2

# Discover the new (last) partition (-r for raw output without tree characters)
PART_NUM=$(lsblk -rno NAME "$LOOP_DEV" | grep "^$(basename "$LOOP_DEV")p" | tail -1 | grep -o '[0-9]*$')
PARTITION="${LOOP_DEV}p${PART_NUM}"

if [ ! -b "$PARTITION" ]; then
    # Force kernel re-read
    partprobe "$LOOP_DEV"
    udevadm settle || sleep 2
fi

if [ ! -b "$PARTITION" ]; then
    echo "ERROR: partition device not found after partprobe: $PARTITION" >&2
    exit 1
fi

echo "Formatting $PARTITION as ext4 (no journal) with label embedded_image ..."
# Create ext4 WITHOUT journal first, so rsync can fill the partition.
# After populating, we add the journal - tune2fs places it near the END of the
# filesystem in the free space, away from the data blocks rsync wrote.
mkfs.ext4 -L embedded_image -O ^has_journal "$PARTITION"

# Mount and populate (no journal means no risk of rsync overwriting journal blocks)
MOUNT_POINT=$(mktemp -d)
mount "$PARTITION" "$MOUNT_POINT"

echo "Copying image store into partition ..."
rsync -a --info=progress2 "$IMAGE_STORE"/ "$MOUNT_POINT"/

echo "Unmounting partition ..."
umount "$MOUNT_POINT"
rmdir "$MOUNT_POINT"
MOUNT_POINT=""

echo "Adding journal to ext4 partition (placed at end of filesystem) ..."
# tune2fs adds the journal using free blocks near the end of the filesystem
tune2fs -O has_journal "$PARTITION"
e2fsck -f -p "$PARTITION" 2>/dev/null || true

losetup -d "$LOOP_DEV"
LOOP_DEV=""

# Convert to qcow2
echo "Converting to qcow2: $OUTPUT_QCOW2"
qemu-img convert -f raw -O qcow2 "$WORK_IMAGE" "$OUTPUT_QCOW2"

rm -f "$WORK_IMAGE"
echo "Done: $OUTPUT_QCOW2"
