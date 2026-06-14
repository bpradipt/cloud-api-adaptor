# Embedded Container Image in CAA PodVM

Builds a podvm qcow2 that carries a pre-cached OCI container image inside its
squashfs rootfs.  At boot, `kata-image-cache.service` mounts the embedded erofs
archive directly at `/run/kata-containers/image/layers` so the patched CDH
daemon can locate the image via its `reference_db` without any registry access.

---

## Architecture

```
Build time (Docker multi-stage → qcow2)
────────────────────────────────────────────────────────────────────────────
Stage 1  cdh-builder     Clone bpradipt/guest-components@image-cache-0.20.0
                         Build CDH daemon  (with reference_db patch) → /cdh-daemon
                         Build cdh-oneshot (ONE_SHOT=true)           → /cdh-oneshot
                         NOTE: ONE_SHOT=true renames cdh-oneshot → confidential-data-hub
                         in target/; save daemon BEFORE running ONE_SHOT.

Stage 2  image-puller    mount tmpfs at /run/kata-containers  (avoids overlay-on-
                         overlay EINVAL inside BuildKit's overlay2 storage)
                         cdh-oneshot pull-image --image-url <IMAGE> --bundle-path /tmp/bundle
                           → layers land in /run/kata-containers/image/layers/ as
                             extracted directories (0/, 1/, …)
                           → /run/kata-containers/image/meta_store.json written
                         mkfs.erofs -z lz4 -E dedupe /layers.erofs
                             /run/kata-containers/image/layers
                         copy meta_store.json → /meta_store.json

Stage 3  mkosi-builder   Standard mkosi build
                         COPY /cdh-daemon → resources/binaries-tree/.../confidential-data-hub
                         COPY /layers.erofs  → mkosi.skeleton/usr/lib/kata-cache/layers.erofs
                         COPY /meta_store.json → mkosi.skeleton/usr/lib/kata-cache/meta_store.json
                         mkosi --profile=embedded --profile=debug
                           Finalise: systemctl enable kata-image-cache.service
                           repart:   squashfs rootfs (1.5 G) + dm-verity (256 M)

Output: system.raw → qemu-img convert → podvm-ubuntu-amd64-embedded.qcow2

Boot time (in VM)
────────────────────────────────────────────────────────────────────────────
kata-image-cache.service   (Before= CDH + kata-agent, instantaneous)
  Step 1: cp /usr/lib/kata-cache/meta_store.json
             /run/kata-containers/image/meta_store.json
             ← always happens first, regardless of erofs mount success
  Step 2: modprobe erofs
  Step 3: mount -t erofs -o ro /usr/lib/kata-cache/layers.erofs
                               /run/kata-cache-erofs      ← private mount point
  Step 4: mkdir -p /run/kata-containers/image/layers      ← writable tmpfs
  Step 5: for each layer N in /run/kata-cache-erofs/:
            mount --bind (ro) /run/kata-cache-erofs/N
                              /run/kata-containers/image/layers/N

CDH daemon   reads meta_store.json → reference_db hit for embedded image
             create_bundle: snapshot.mount(bind-mounted layer dirs as lower-dir)
             → overlay mount succeeds, container bundle ready
kata-agent   requests image → CDH resolves from local cache (no network pull)
```

### Why bind-mounts (not direct erofs mount)

Three designs were tried before settling on the current bind-mount approach:

**Design 1: `cp -a` to tmpfs** — copying hundreds of MB of layers before
kata-agent could start delayed startup by several minutes.

**Design 2: direct erofs mount at the CDH layer store path**
(`mount -t erofs /usr/lib/kata-cache/layers.erofs /run/kata-containers/image/layers`)
This hit two problems:
- Overlay-on-overlay restriction — image-rs creates container snapshots by
  mounting an overlay with the layer store paths as lower-dirs; an overlayfs
  path cannot itself be an overlayfs lower-dir.
- **More critically**: because the layer store path was read-only (erofs), any
  CDH code path that attempted to create a new layer directory (e.g., a cache
  miss for a different image, or a failed reference_db lookup) would hit EROFS,
  causing `create_dir_all` to fail and the rollback (`remove_dir_all`) to fail
  with ENOENT because the directory was never created — producing the opaque
  "Failed to decode layer data stream: Failed to roll back when unpacking" error.

**Design 3 (current): bind-mount individual layers onto a writable tmpfs**

```
erofs archive at → /run/kata-cache-erofs/          (private, read-only)
CDH layer store  → /run/kata-containers/image/layers/  (tmpfs, writable)
   layers/0      → bind-mount from /run/kata-cache-erofs/0 (read-only)
   layers/1      → bind-mount from /run/kata-cache-erofs/1 (read-only)
   ...
   layers/<n>    → plain tmpfs dir for new network-pulled layers
```

`meta_store.json` is also copied **before** the erofs mount attempt so that
CDH always gets a populated reference_db even if the mount fails.

### Key paths in the finished VM

| Path | Content |
|---|---|
| `/usr/lib/kata-cache/layers.erofs` | read-only erofs archive of all OCI image layers |
| `/usr/lib/kata-cache/meta_store.json` | image-rs reference database (in squashfs, survives reboots) |
| `/run/kata-cache-erofs/` | private erofs mount point (runtime, created by kata-image-cache-setup) |
| `/run/kata-containers/image/layers/0…N` | writable tmpfs with read-only bind mounts from erofs |
| `/run/kata-containers/image/meta_store.json` | copy of meta_store.json for CDH (on tmpfs) |
| `/usr/local/bin/confidential-data-hub` | patched CDH daemon from the fork (reference_db support) |
| `/usr/local/bin/kata-image-cache-setup` | boot script: copies meta_store.json, binds layer dirs |
| `/usr/lib/systemd/system/kata-image-cache.service` | systemd unit (enabled only in embedded profile) |

---

## How to Build

### Prerequisites

- Docker with `insecure-builder` configured for both `security.insecure` and
  `network.host` (quay.io pull during image-puller stage needs host network):

```bash
docker buildx rm insecure-builder 2>/dev/null || true
docker buildx create \
  --driver-opt image=moby/buildkit:master \
  --name insecure-builder \
  --buildkitd-flags '--allow-insecure-entitlement security.insecure \
                     --allow-insecure-entitlement network.host'
```

- Standard binaries must be built first (populates `resources/binaries-tree/`):

```bash
cd src/cloud-api-adaptor/podvm-mkosi
PODVM_DISTRO=ubuntu make binaries
```

### Build the embedded image

```bash
cd src/cloud-api-adaptor/podvm-mkosi

PODVM_DISTRO=ubuntu \
EMBEDDED_IMAGE="quay.io/bpradipt/kata-vm-image:7may" \
make image-embedded
```

Output:
```
build/system.raw                         (2.3 GB raw disk)
build/podvm-ubuntu-amd64-embedded.qcow2  (1.8 GB compressed qcow2)
```

### Makefile variables

| Variable | Default | Description |
|---|---|---|
| `EMBEDDED_IMAGE` | `docker.io/library/nginx:latest` | OCI image to pre-cache |
| `GC_REPO` | `https://github.com/bpradipt/guest-components.git` | guest-components fork |
| `GC_BRANCH` | `image-cache-0.20.0` | branch with reference_db patch |

---

## Running a Pod

### Cluster-side requirements

The embedded podvm UKI bundles a 62.6 MB compressed initrd.  UEFI needs at
least ~4 GB of RAM to load it (256 MB causes `EFI_OUT_OF_RESOURCES`).  Set
the pod's memory request/limit to 4 Gi, which CAA passes directly to the VM:

```yaml
resources:
  requests:
    memory: "4Gi"
  limits:
    memory: "4Gi"
```

### One-time worker-node configuration

After cluster setup, increase two timeouts on the worker node so long VM boot
times do not cause premature failures:

```bash
# 1. kata-remote dial_timeout (default 30 s — too short for 4-min boot)
sudo sed -i 's/dial_timeout = [0-9]*/dial_timeout = 600/' \
  /opt/kata/share/defaults/kata-containers/configuration-remote.toml

# 2. kubelet runtimeRequestTimeout (default 2 min)
# Edit /var/lib/kubelet/config.yaml and set:
#   runtimeRequestTimeout: 10m
# then restart kubelet:
sudo systemctl restart kubelet
```

### Example pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: embedded-image-test
spec:
  runtimeClassName: kata-remote
  containers:
  - name: test
    image: quay.io/bpradipt/kata-vm-image:7may   # the embedded image
    command: ["sh", "-c", "echo 'running from embedded cache' && sleep 300"]
    resources:
      requests:
        memory: "4Gi"
      limits:
        memory: "4Gi"
  restartPolicy: Never
```

### Expected boot timeline (4 GB VM, 2.3 GB squashfs)

| Time | Event |
|---|---|
| T+0 s | CAA creates the VM (COW overlay on podvm-base.qcow2) |
| T+30 s | UEFI loads 78 MB UKI into memory |
| T+60 s | Kernel starts, dm-verity verifies 1.5 GB squashfs |
| T+90 s | systemd starts; kata-image-cache.service mounts erofs (<1 s) |
| T+120 s | CDH starts, reads meta_store.json |
| T+~240 s | APF connects to worker node; kata-agent ready |
| T+~250 s | Image pull request arrives; CDH resolves from reference_db |

---

## End-to-End Test Results (2026-06-14)

**Status**: ✅ WORKING — container starts and runs from embedded cache.

### Verified timeline (libvirt, 4 GB VM, 2.3 GB squashfs)

| Time | Event |
|---|---|
| T+0 s | CAA creates VM (COW overlay on podvm-base.qcow2) |
| T+20 s | VM created, APF connects |
| T+27 s | CreateContainer — CDH finds image in reference_db, overlay mounted |
| T+28 s | StartContainer — container prints "Container running!" |

CDH resolves `quay.io/bpradipt/kata-vm-image:7may` from the embedded
reference_db on first request (~13 s vs a full network pull of 700 MB).

### Root cause of the previous "Failed to roll back when unpacking" error

The original design mounted erofs directly at `/run/kata-containers/image/layers`.
Any CDH code path that tried to create a new layer directory at that path hit
EROFS (read-only filesystem).  When CDH then tried to roll back via
`remove_dir_all` on the directory that was never created, it got ENOENT —
producing the opaque error message.

**Fix**: bind-mount each cached layer directory onto a **writable tmpfs** layer
store (see the bind-mount design above).  The layer store path remains writable
so CDH can create new layer directories for images not in the embedded cache,
while pre-cached layers are exposed read-only via individual bind mounts.

---

## Files Added / Modified

### New files

| File | Purpose |
|---|---|
| `Dockerfile.mkosi.embedded.ubuntu` | Multi-stage Dockerfile (cdh-builder → image-puller → mkosi-builder) |
| `mkosi.images/system/mkosi.profiles/embedded.conf` | Sets `EMBEDDED_IMAGE_CACHE=true` env for finalize script |
| `mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/kata-image-cache.service` | Systemd unit (enabled only in embedded profile) |
| `mkosi.images/system/mkosi.skeleton/usr/local/bin/kata-image-cache-setup` | Boot script: direct erofs mount at layer store path |
| `mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/kata-agent.service.d/10-image-cache.conf` | Soft `Wants=/After=kata-image-cache.service` |
| `mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/confidential-data-hub.service.d/10-image-cache.conf` | Same for CDH |

### Modified files

| File | Change |
|---|---|
| `Makefile` | Added `image-embedded` target + `EMBEDDED_IMAGE`, `GC_REPO`, `GC_BRANCH` vars |
| `mkosi.images/system/mkosi.finalize.chroot` | `systemctl enable kata-image-cache.service` when `EMBEDDED_IMAGE_CACHE=true` |
| `mkosi.images/system/mkosi.repart/20-root-verity.conf` | `SizeMinBytes=256M SizeMaxBytes=256M` (up from 64M) — the 1.5 G squashfs produces a ~104 M dm-verity hash tree |

---

## Build Bugs Fixed

| # | Symptom | Root cause | Fix |
|---|---|---|---|
| 1 | `fatal: Remote branch local-0.20.0 not found` | Branch is `image-cache-0.20.0` | Use correct branch name |
| 2 | Binary path not found in COPY | Cargo workspace root is `/guest-components/target/`, not inside the sub-crate | Save to `/cdh-daemon` and `/cdh-oneshot` at container root |
| 3 | CDH daemon binary overwritten | `ONE_SHOT=true` renames `cdh-oneshot → confidential-data-hub` in `target/` | Save daemon before running `ONE_SHOT=true` |
| 4 | `error: unexpected argument '--config'` | cdh-oneshot CLI changed; `--config <toml>` removed | Use `--image-url <URL> --bundle-path <PATH>` |
| 5 | `EINVAL` on overlay mount in BuildKit | BuildKit uses overlay2; overlay upper-dir can't sit on another overlay | `mount -t tmpfs tmpfs /run/kata-containers` before running cdh-oneshot |
| 6 | `error sending request (https://quay.io/...)` | BuildKit container can't reach quay.io by default | `RUN --network=host`; add `network.host` entitlement to insecure-builder |
| 7 | `unable to get local issuer certificate` | musl static binary looks for CAs at `/etc/ssl/cert.pem` (Alpine path) | Symlink from Ubuntu path; set `SSL_CERT_FILE` |
| 8 | Verity partition too small | mkosi pre-sizes disk from `SizeMinBytes`; 1.5 G squashfs needs ~104 M hash tree | `SizeMinBytes=256M SizeMaxBytes=256M` |
| 9 | VM won't boot — `EFI_OUT_OF_RESOURCES` | UKI carries 62.6 MB initrd; UEFI needs 4 GB+ RAM to load it | Pod spec: `memory: "4Gi"` |
| 10 | `dial_timeout exceeded` | kata-remote dial_timeout defaults to 30 s; VM boot takes ~4 min | Set `dial_timeout = 600` in `configuration-remote.toml` |
| 11 | Sandbox killed at ~2 min | kubelet `runtimeRequestTimeout` defaults to 2 min | Set `runtimeRequestTimeout: 10m` in kubelet config |
| 12 | `CDH overlay snapshot blocked` | overlay-over-erofs triggers overlay-on-overlay kernel restriction | Bind-mount individual layers from erofs onto writable tmpfs layer store |
| 13 | `kata-agent` delayed by minutes | `cp -a` of large layer dirs to tmpfs blocked kata-agent startup | Direct erofs mount completes instantly |
| 14 | `Failed to decode layer data stream: Failed to roll back when unpacking` | Erofs mounted at CDH layer store path → `create_dir_all` fails (EROFS) → rollback `remove_dir_all` fails (ENOENT) | Bind-mount approach: layer store is writable tmpfs, pre-cached layers bind-mounted from erofs |
