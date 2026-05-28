# Embedded Container Image in PodVM Design

**Date:** 2026-05-28
**Status:** Approved

## Goal

Embed a pre-unpacked container image inside the podvm disk image so that kata-agent's image-rs
finds it already cached at `/run/kata-containers/image` on boot — eliminating the container image
pull from the pod startup critical path.

**Test image:** `quay.io/bpradipt/cuda-samples:ubi9` (3.6 GB)
**Measurement:** Wall-clock time from pod creation to `Running` state, compared between a generic
podvm image and an embedded podvm image, using the libvirt provider.

## Background

- kata-agent uses image-rs to pull container images into `/run/kata-containers/image` at pod start.
- For large images (3.6 GB) this pull dominates startup latency.
- The mkosi-based podvm disk has three partitions: ESP (vfat), root (squashfs + dm-verity,
  read-only), and scratch (`trusted_store`, ext4, LUKS-encrypted at first boot, mounted at
  `/run/kata-containers/image` when scratch space is enabled).
- image-rs needs **write access** to `/run/kata-containers/image` even for cached images (lock
  files, metadata updates), so any read-only solution (e.g. embedding in the squashfs root) does
  not work.

## Approach: Dedicated ext4 Partition (Post-process the raw image)

Build the normal mkosi image unchanged, then a post-processing shell script appends a dedicated
writable ext4 partition (labeled `embedded_image`) pre-populated with the unpacked image store.
A conditional systemd mount unit — already present in the mkosi skeleton for all images — mounts
this partition at `/run/kata-containers/image` before kata-agent starts, but only when the
partition label exists. Generic images are unaffected.

Rejected alternatives:
- **Embed in root squashfs** — root partition is read-only (dm-verity); image-rs writes fail.
- **mkosi repart integration** — avoids double-inclusion of image data in squashfs requires
  careful RemoveFiles/CopyFiles ordering; fragile and hard to debug.
- **Second disk via libvirt CAA** — cleanest long-term but requires CAA provider changes; out of
  scope for a benchmarking test.

## Architecture

```
BUILD TIME
──────────────────────────────────────────────────────────

  make download-embedded-image
    └─ docker run quay.io/bpradipt/kata-initrd-debug-tools:latest
         /tools/<binary> quay.io/bpradipt/cuda-samples:ubi9
         → src/cloud-api-adaptor/podvm-mkosi/resources/embedded-image/
           (OCI image store in the format image-rs expects)

  make image-embedded
    ├─ [1] normal mkosi build → build/system.raw
    │       (identical to production image)
    └─ [2] hack/embed-image.sh (post-processing):
           ├─ fallocate: extend system.raw by IMAGE_SIZE + buffer
           ├─ sfdisk --append: add GPT partition
           │     type=linux-generic, name=embedded_image
           ├─ losetup -P: attach raw image as loop device
           ├─ mkfs.ext4 -L embedded_image: format new partition
           ├─ mount + rsync: copy resources/embedded-image/ into partition
           ├─ umount + losetup -d: clean up
           └─ qemu-img convert: system.raw → podvm-ubuntu-amd64-embedded.qcow2

BOOT TIME (embedded variant only)
──────────────────────────────────────────────────────────

  systemd-repart           (grows scratch partition if marker present)
       ↓
  run-kata-containers-image.mount
       ConditionPathExists=/dev/disk/by-label/embedded_image
       What=/dev/disk/by-label/embedded_image
       Where=/run/kata-containers/image
       Type=ext4, Options=rw
       Before=kata-agent.service
       After=systemd-repart.service local-fs.target
       ↓
  kata-agent.service starts
       └─ image-rs checks /run/kata-containers/image
          finds cuda-samples:ubi9 already present → no pull

BOOT TIME (generic variant)
──────────────────────────────────────────────────────────

  run-kata-containers-image.mount
       ConditionPathExists=/dev/disk/by-label/embedded_image  ← FALSE
       (unit is a no-op; image-rs pulls normally)

TEST
──────────────────────────────────────────────────────────

  Upload both qcow2 images to libvirt as separate volumes.
  Deploy pod A referencing generic podvm → record time to Running.
  Deploy pod B referencing embedded podvm → record time to Running.
  Compare startup delta.
```

## Components

### 1. `make download-embedded-image` (new Makefile target)

Runs the prebuilt download/unpack binary from `quay.io/bpradipt/kata-initrd-debug-tools:latest`
via `docker run`. Output lands in `resources/embedded-image/` relative to the mkosi directory.
This target is run once by the developer; the output directory is cached between builds and is
`.gitignore`d.

Binary: `/tools/image_pull_debug` inside the tools image.
The `--work-dir` flag sets image-rs's layer storage root — this is the directory that gets
embedded in the partition and mounted at `/run/kata-containers/image` at runtime.

Example invocation:
```bash
docker run --rm \
  -v $(pwd)/resources/embedded-image:/output \
  quay.io/bpradipt/kata-initrd-debug-tools:latest \
  /tools/image_pull_debug \
    --image quay.io/bpradipt/cuda-samples:ubi9 \
    --work-dir /output
```

### 2. `hack/embed-image.sh` (new script)

Post-processing script that takes `build/system.raw` and `resources/embedded-image/` as inputs
and produces `build/podvm-ubuntu-amd64-embedded.qcow2`. Steps:

1. Compute required partition size from `resources/embedded-image/` (du + 10% buffer).
2. Extend `system.raw` with `fallocate`.
3. Append a new GPT partition entry with `sfdisk --append`.
4. Attach with `losetup -P`, format with `mkfs.ext4 -L embedded_image`.
5. Mount, `rsync -a` the image store contents, unmount, detach loop device.
6. Convert to qcow2 with `qemu-img convert -f raw -O qcow2`.

Requires root (loop device + mount). Can be run inside Docker with `--privileged` or directly on
the host with `sudo`.

### 3. `make image-embedded` (new Makefile target)

Chains `make image` (normal mkosi build) followed by `hack/embed-image.sh`. Outputs
`build/podvm-ubuntu-amd64-embedded.qcow2` alongside the normal `build/podvm-ubuntu-amd64.qcow2`.

### 4. Systemd mount unit (new, shipped in mkosi skeleton)

File: `mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/run-kata-containers-image.mount`

```ini
[Unit]
Description=Mount embedded container image store
ConditionPathExists=/dev/disk/by-label/embedded_image
After=systemd-repart.service local-fs.target
Before=kata-agent.service

[Mount]
What=/dev/disk/by-label/embedded_image
Where=/run/kata-containers/image
Type=ext4
Options=rw

[Install]
WantedBy=multi-user.target
```

This unit is enabled in the preset for all images. On generic images the `ConditionPathExists`
check fails and the unit is skipped entirely. On embedded images the partition is mounted
read-write so image-rs can write lock files and metadata alongside the pre-populated layers.

The mount creates `/run/kata-containers/image` implicitly; `kata-agent.service` already has
`ExecStartPre=mkdir -p /run/kata-containers` which is idempotent.

### 5. Preset entry

Add to `mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system-preset/30-coco.preset`:

```
enable run-kata-containers-image.mount
```

## Data Flow at Boot

```
systemd-repart
  → (maybe) formats trusted_store partition
  → our mount unit condition check: /dev/disk/by-label/embedded_image exists?
      yes → mount ext4 partition at /run/kata-containers/image
      no  → skip
  → kata-agent starts
      → image-rs: does /run/kata-containers/image/<image-ref> exist?
          yes → use cached layers, start container immediately
          no  → pull from registry (normal flow)
```

## Testing Plan

1. Build generic image: `PODVM_DISTRO=ubuntu make image` → `podvm-ubuntu-amd64.qcow2`
2. Download embedded image store: `make download-embedded-image`
3. Build embedded image: `make image-embedded` → `podvm-ubuntu-amd64-embedded.qcow2`
4. Upload both to libvirt: `virsh vol-create-as` for each.
5. Configure two CAA deployments (or two node annotations) pointing at different volumes.
6. Deploy identical pods requesting `quay.io/bpradipt/cuda-samples:ubi9`.
7. Measure: `kubectl get pod -w` timestamps from creation to `Running`.
8. Repeat 3 times each, report median.

## Key Constraints

- The mount unit must fire **before** `kata-agent.service`. The `Before=kata-agent.service`
  directive and the `After=systemd-repart.service` directive together ensure correct ordering.
- The ext4 partition must be labeled `embedded_image` (used as the stable identifier; UUID
  changes on every build).
- `resources/embedded-image/` is `.gitignore`d (3.6 GB, not committed).
- The post-processing script requires loop device access; run with `sudo` on host or inside
  `--privileged` Docker.
- The generic image produced by the normal mkosi build is byte-for-byte identical to the current
  production image — no changes to the mkosi build itself.
