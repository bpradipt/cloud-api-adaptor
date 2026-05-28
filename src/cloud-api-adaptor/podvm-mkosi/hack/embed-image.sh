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

# Work on a copy so system.raw remains usable for the generic qcow2
WORK_IMAGE="${RAW_IMAGE%.raw}-embedded.raw"
echo "Copying $RAW_IMAGE -> $WORK_IMAGE ..."
cp "$RAW_IMAGE" "$WORK_IMAGE"

# Calculate partition size: image store size + 20% headroom (minimum 512 MB)
IMAGE_SIZE_MB=$(du -sm "$IMAGE_STORE" | cut -f1)
HEADROOM_MB=$(( IMAGE_SIZE_MB / 5 ))
HEADROOM_MB=$(( HEADROOM_MB < 512 ? 512 : HEADROOM_MB ))
PARTITION_SIZE_MB=$(( IMAGE_SIZE_MB + HEADROOM_MB ))

echo "Image store: ${IMAGE_SIZE_MB} MB, partition: ${PARTITION_SIZE_MB} MB"

# Extend the raw image file to make room for the new partition
CURRENT_BYTES=$(stat -c%s "$WORK_IMAGE")
NEW_BYTES=$(( CURRENT_BYTES + PARTITION_SIZE_MB * 1024 * 1024 ))
fallocate -l "$NEW_BYTES" "$WORK_IMAGE"

# Append a new GPT partition using all newly added space
echo "type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=embedded_image" \
    | sfdisk --append "$WORK_IMAGE"

# Attach as loop device with partition scanning
LOOP_DEV=$(losetup -f --show -P "$WORK_IMAGE")
echo "Attached as $LOOP_DEV"

# Give the kernel time to create the partition device node
udevadm settle || sleep 2

# Discover the new (last) partition (-r for raw output without tree characters)
PART_NUM=$(lsblk -rno NAME "$LOOP_DEV" | grep -c "^$(basename "$LOOP_DEV")p")
PARTITION="${LOOP_DEV}p${PART_NUM}"

if [ ! -b "$PARTITION" ]; then
    # Force kernel re-read
    partprobe "$LOOP_DEV"
    udevadm settle || sleep 2
fi

echo "Formatting $PARTITION as ext4 with label embedded_image ..."
mkfs.ext4 -L embedded_image "$PARTITION"

# Mount and populate
MOUNT_POINT=$(mktemp -d)
mount "$PARTITION" "$MOUNT_POINT"

echo "Copying image store into partition ..."
rsync -a --info=progress2 "$IMAGE_STORE"/ "$MOUNT_POINT"/

# Clean up loop device
umount "$MOUNT_POINT"
rmdir "$MOUNT_POINT"
losetup -d "$LOOP_DEV"

# Convert to qcow2
echo "Converting to qcow2: $OUTPUT_QCOW2"
qemu-img convert -f raw -O qcow2 "$WORK_IMAGE" "$OUTPUT_QCOW2"

rm -f "$WORK_IMAGE"
echo "Done: $OUTPUT_QCOW2"
