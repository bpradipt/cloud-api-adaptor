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
  modprobe erofs
  mount -t erofs -o ro /usr/lib/kata-cache/layers.erofs
                        /run/kata-containers/image/layers
                          ← direct erofs mount (not overlay); erofs is a valid
                            overlay lower-dir source and mount completes in <1 s
  cp /usr/lib/kata-cache/meta_store.json
     /run/kata-containers/image/meta_store.json

CDH daemon   reference_db patch finds image in meta_store.json → CDH knows
             the image is pre-cached at /run/kata-containers/image/layers/
kata-agent   requests image → CDH resolves from local cache (no network pull)
```

### Why direct erofs mount (not an overlay)

An earlier design mounted an overlayfs at the layer store path:
```
lower=erofs, upper=tmpfs → /run/kata-containers/image/layers
```
This hit two problems:
1. `cp -a` latency — copying hundreds of MB of layers to tmpfs blocked
   `kata-agent.service` from starting for several minutes.
2. Overlay-on-overlay restriction — Linux forbids using an overlayfs-backed
   path as the lower-dir of another overlayfs, which is exactly what image-rs
   does when creating a container snapshot.

Mounting the erofs **directly** at the layer store path is instantaneous and
avoids both issues.  The mount is read-only; new network-pulled layers go to a
separate tmpfs path managed by CDH.

### Key paths in the finished VM

| Path | Content |
|---|---|
| `/usr/lib/kata-cache/layers.erofs` | read-only erofs archive of all OCI image layers |
| `/usr/lib/kata-cache/meta_store.json` | image-rs reference database (in squashfs, survives reboots) |
| `/usr/local/bin/confidential-data-hub` | patched CDH daemon from the fork (reference_db support) |
| `/usr/local/bin/kata-image-cache-setup` | boot script: mounts erofs directly at layer store path |
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

## Known Issue: Layer Serving from erofs-backed Path

**Status**: Infrastructure works (erofs mounts, CDH reads meta_store.json,
reference_db hit confirmed); end-to-end container start not yet verified.

**Symptom** seen during testing:
```
[CDH] [ERROR]: Image Pull error: Failed to decode layer data stream:
Failed to roll back when unpacking
```

**Context**: The same extracted-directory format from `cdh-oneshot` works
correctly in the Kata/QEMU `oci2kata` setup (the original design this is
based on).  The format itself is not the bug.

**Suspected difference vs Kata/QEMU**: In Kata/QEMU the layer store lives on
**tmpfs** (image-rs extracts directly there).  In CAA we mount **erofs** at
the same path, so image-rs reads through an erofs filesystem rather than
tmpfs.  erofs is read-only and has limited xattr support; if image-rs relies
on xattrs or infers format from filesystem metadata, it may behave differently
on erofs than on tmpfs.

**Note on the test image**: `quay.io/bpradipt/kata-vm-image:7may` is a Kata
runtime artifact image (contains kernel + initrd files), not a general-purpose
application container.  This is the same image oci2kata uses for its end-to-end
test, where it works.  The failure in CAA is therefore environment-specific,
not image-specific.

**Next debugging steps**:
1. SSH into a live VM (debug profile enables sshd; authorized_keys must be
   configured) and check `systemctl status kata-image-cache.service` and
   CDH journal entries to confirm the erofs mount and meta_store.json path.
2. Try embedding and running a plain application container (e.g. `nginx:latest`)
   to isolate whether the issue is erofs xattr behaviour or something specific
   to the kata-vm-image artifact format.
3. Compare `stat` and xattr output for a layer directory on erofs vs tmpfs;
   patch `kata-image-cache-setup` to bind-mount individual layer dirs from erofs
   onto a tmpfs tree if erofs xattr behaviour is the cause.

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
| 12 | `CDH overlay snapshot blocked` | overlay-over-erofs triggers overlay-on-overlay kernel restriction | Mount erofs directly at layer store (no wrapping overlay) |
| 13 | `kata-agent` delayed by minutes | `cp -a` of large layer dirs to tmpfs blocked kata-agent startup | Direct erofs mount completes instantly |
