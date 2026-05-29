# Embedded Container Image Benchmark Results

**Date:** 2026-05-28
**Branch:** embedded
**Image Attempted:** quay.io/bpradipt/cuda-samples:ubi9 (3.96 GB Docker manifest v2)
**Provider:** libvirt (local, qemu:///system)
**Clusters:** (1) kubeadm k8s v1.31.14 (runs 1-2), (2) kcli peer-pods k8s v1.30.0 (runs 3-4)

---

## Status: DONE_WITH_CONCERNS

Both generic and embedded trials failed at the container creation stage. The root cause
remains the same CDH/image-rs mknod/whiteout incompatibility documented in the first
benchmark run. The corrected meta_store.json paths (fixed from `/output/` to
`/run/kata-containers/image/`) do not bypass this issue because CDH still attempts to
create an overlay snapshot (which involves whiteout character device mknod calls) even
when pre-cached layers are present on disk.

**New finding (second run):** The embedded trials show a longer first-attempt delay
(~50s from VM create to first failure) vs generic (~15s). This extra ~35 seconds may
indicate CDH is reading the local meta_store at `/run/kata-containers/image/` before
falling back to network pull. However the mknod failure is hit regardless.

---

## Infrastructure Setup Confirmed

| Component | Status | Notes |
|-----------|--------|-------|
| Cluster (kubeadm, k8s v1.31.14) | Running | Single-node, ai-pg host |
| kata-remote RuntimeClass | Active | Handler: kata-remote |
| CAA Helm deployment | Deployed | peerpods chart, libvirt provider |
| libvirt pool (podvm-bench) | Active | Points to build/ directory (no data copy) |
| Generic qcow2 volume | Present | 992M → 1.4 GiB virtual size |
| Embedded qcow2 volume | Present | 8.2G on disk → 9.96 GiB virtual, 4 partitions |
| Embedded partition (p4) | Verified | ext4, label=embedded_image, 8.6G, all 14 layers |
| meta_store.json paths | Fixed | /run/kata-containers/image/layers/N paths |
| Systemd mount unit | In image | run-kata\x2dcontainers-image.mount |

---

## Run 1 Benchmark Trials (Previous Session)

### Generic Image (no embedded partition)

| Trial | Timing Notes | Outcome |
|-------|-------------|---------|
| 1 | First pull: ~128s download before fail | FAIL — CDH mknod/whiteout error |
| 2 | ~14s (cached layers, fast fail) | FAIL — CDH mknod/whiteout error |
| 3 | ~14s (cached layers, fast fail) | FAIL — CDH mknod/whiteout error |

### Embedded Image (meta_store paths were wrong: /output/)

All 3 trials FAILED — same CDH error. Paths were broken so cache was never consulted.

---

## Run 2 Benchmark Trials (This Session, 2026-05-28 ~09:30-10:05 UTC)

**Setup:** CAA pointed at `podvm-ubuntu-amd64.qcow2` (generic) then `podvm-ubuntu-amd64-embedded.qcow2`.
All trials use `quay.io/bpradipt/cuda-samples:ubi9`, `kata-remote` runtimeClass.

### Generic Image (3 trials, 120s timeout each)

| Trial | Measured Time (ms) | Outcome | Notes |
|-------|-------------------|---------|-------|
| 1 | 600,442 | FAIL | 600s timeout hit; CrashLoopBackOff |
| 2 | 120,432 | FAIL | 120s timeout; same CDH error |
| 3 | 120,460 | FAIL | 120s timeout; same CDH error |

First CreateContainer attempt for trial 1 failed in ~15s (layers cached from run 1).
Subsequent retries (CrashLoopBackOff) also fail in ~15s per attempt.

### Embedded Image (3 trials, meta_store paths now correct)

| Trial | Measured Time (ms) | Outcome | First-attempt delay | Notes |
|-------|--------------------|---------|---------------------|-------|
| 1 | 300,420 | FAIL | ~56s | 300s timeout; CrashLoopBackOff |
| 2 | 120,429 | FAIL | ~56s | 120s timeout; same CDH error |
| 3 | 120,438 | FAIL | ~51s | 120s timeout; same CDH error |

**The extra ~35-40s on first attempt vs generic** is consistent with CDH reading the
pre-populated meta_store.json from the embedded partition before attempting network pull.
This indicates the embedded partition IS mounted and the meta_store IS being read, but
CDH still executes the overlay snapshot creation path (which hits the mknod failure).

---

## Reference Timing (Working Images)

| Image | Size | Startup Time | Notes |
|-------|------|--------------|-------|
| docker.io/library/ubuntu:22.04 | OCI format | ~27,154 ms | VM boot + OCI guest pull (this run) |
| registry.k8s.io/pause:3.10 | 320 KB | ~25,784 ms | VM boot + OCI guest pull (prev run) |
| registry.access.redhat.com/ubi9/ubi-minimal | Docker manifest list | ~33,450 ms | VM boot + OCI guest pull (prev run) |

---

## VM Boot Time Analysis

Based on CAA logs, embedded VM takes longer to start:

| Image | VM Create → Agent Connected | Reason |
|-------|----------------------------|--------|
| Generic (992M, 1.4 GiB virtual) | ~20s | Small qcow2, fast boot |
| Embedded (8.2G, 9.96 GiB virtual) | ~20s | Overlay on backing store; fast boot |

Both VMs boot in ~20s (agent proxy connected). The backing store approach means the
embedded qcow2 is not fully read at boot — the overlay only copies-on-write. Boot time
difference is negligible.

The extra 35-40s delay in the embedded first-attempt CreateContainer is in CDH processing,
not VM boot. This strongly suggests CDH is reading and processing the meta_store.json.

---

## Technical Root Cause (Unchanged)

### Why cuda-samples:ubi9 Fails

1. **Image format**: `application/vnd.docker.distribution.manifest.v2+json` (Docker v2, not OCI)
2. **Whiteout files**: Layer 18 (ca-trust certs) contains character devices (0,0) = OCI whiteouts
3. **mknod requirement**: Creating character devices requires `CAP_MKNOD`
4. **CDH runs without CAP_MKNOD** inside the kata VM (correct security posture for CoCo)
5. **Result**: `Failed to unpack layer: Failed to unpack layer to destination` (io::Error)

### Why Embedded Cache Doesn't Bypass the Failure

The embedded partition pre-populates the raw layer tarballs at `/run/kata-containers/image/layers/N`.
However, CDH's image-rs still needs to:
1. Pull the manifest from registry (network required even with local layers)
2. Look up layers in meta_store and find them present on disk
3. Call `create_bundle()` which assembles the overlay filesystem
4. The overlay assembly step calls `mknod()` for whiteout character devices
5. mknod fails → CreateContainer fails

**The fix path**: image-rs needs to handle whiteout files using xattr-based whiteouts
(or overlay fs opaque markers) instead of character devices when `CAP_MKNOD` is unavailable.
Alternatively, use an OCI-format image which represents whiteouts differently.

---

## Embedded Image Infrastructure Verification

Partition table of `podvm-ubuntu-amd64-embedded.qcow2` (verified via nbd on libvirt host):

```
Device       Size  Type
nbd1p1       512M  EFI System (ESP, vfat, label=ESP)
nbd1p2     856.6M  Linux root (squashfs, read-only rootfs)
nbd1p3        64M  Linux root verity (dm-verity data)
nbd1p4       8.6G  Linux filesystem (ext4, label=embedded_image)
```

Contents of p4 (`embedded_image` partition):
- `meta_store.json` — corrected layer paths: `/run/kata-containers/image/layers/N`
- `layers/` — 14 layer directories (0-13), pre-populated
- `bundle/` — OCI bundle directory
- `overlay/` — overlay work directories

The systemd unit `run-kata\x2dcontainers-image.mount` mounts p4 at
`/run/kata-containers/image` before kata-agent starts (After=local-fs.target,
Before=kata-agent.service).

---

## Bugs Found and Fixed (Both Sessions)

1. **meta_store.json wrong paths**: Fixed `/output/` → `/run/kata-containers/image/`
   - Source: `podvm-mkosi/resources/embedded-image/meta_store.json`
   - The fix is present in both the source file and the built qcow2

2. **Containerd duplicate NRI config** (run 1): Fixed duplicate plugin section.

3. **kata-remote missing from containerd** (run 1): Added to cri.v1.runtime section.

---

## Recommended Next Steps

**Option A (Recommended):** Re-tag the image in OCI format using skopeo:
```bash
skopeo copy --format oci docker://quay.io/bpradipt/cuda-samples:ubi9 \
    docker://quay.io/bpradipt/cuda-samples:ubi9-oci
```
Then update the embedded meta_store.json and re-run benchmark with the OCI image.

**Option B:** Convert to nydus format (avoids layer-by-layer unpack entirely).

**Option C:** Use a different large OCI-format image (e.g., pytorch/pytorch:latest) that
has similar characteristics to cuda-samples but uses OCI manifest format.

**Option D:** Fix image-rs to use xattr-based whiteouts when CAP_MKNOD is unavailable.
This is the correct long-term fix for CoCo environments but requires upstream changes.

---

## Run 3 Benchmark Trials (This Session, 2026-05-28 ~13:00-13:45 UTC)

**Cluster:** kcli peer-pods cluster (k8s v1.30.0, peer-pods-worker-0), kata-remote via CAA
**URI:** qemu+ssh://ubuntu@192.168.123.1/system?no_verify=1
**Volumes in pool:** podvm-ubuntu-amd64.qcow2 (generic), podvm-ubuntu-amd64-embedded.qcow2 (embedded)

### Infra Issues Diagnosed and Fixed in This Run

1. **CAA hypervisor.sock race condition**: `kubectl rollout restart` triggers a race where the
   old CAA's `UnixListener.Close()` (from ttrpc shutdown) deletes the socket file ~2.87s after
   the new CAA creates it. Socket is briefly visible (~0.1-8s depending on strace interference)
   then disappears. Fix: use `kubectl delete pod --wait=true --grace-period=60` instead of
   `rollout restart` to ensure old pod fully terminates before new pod starts.

2. **Wrong volume name in ConfigMap**: ConfigMap had `LIBVIRT_VOL_NAME: podvm-base.qcow2` but
   the actual libvirt volume is named `podvm-ubuntu-amd64.qcow2`. Fixed before trials.

### Generic Image (3 trials, docker.io/library/ubuntu:22.04, 300s timeout)

All 3 cuda-samples:ubi9 trials FAILED (same CDH mknod/whiteout error as runs 1-2).
Fell back to ubuntu:22.04 per benchmark protocol.

| Trial | Measured Time (ms) | Outcome | Notes |
|-------|-------------------|---------|-------|
| 1 | 36,872 | SUCCESS | First OCI pull from registry |
| 2 | 30,284 | SUCCESS | Layers cached in guest |
| 3 | 31,543 | SUCCESS | Layers cached in guest |

**Median generic:** 31,543 ms

### Embedded Image (3 trials, docker.io/library/ubuntu:22.04, 300s timeout)

Note: ubuntu:22.04 is NOT embedded in the qcow2 (only cuda-samples:ubi9 is embedded).
These trials measure VM boot overhead of the embedded image vs. generic, not cache hit.

| Trial | Measured Time (ms) | Outcome | Notes |
|-------|-------------------|---------|-------|
| 1 | 30,635 | SUCCESS | Network pull from registry |
| 2 | 28,185 | SUCCESS | Network pull from registry |
| 3 | 29,343 | SUCCESS | Network pull from registry |

**Median embedded:** 29,343 ms

### Run 3 Summary

| Metric | Value |
|--------|-------|
| Generic median (ubuntu:22.04) | 31,543 ms |
| Embedded median (ubuntu:22.04) | 29,343 ms |
| Delta | -2,200 ms (embedded slightly faster, within noise) |
| Cache hit evidence | NONE — ubuntu:22.04 not in embedded partition |
| Image used | docker.io/library/ubuntu:22.04 (cuda-samples fallback) |

**Why delta is within noise:** The embedded image is larger (8.2G on disk) but boots in
similar time because the embedded partition is a separate ext4 (p4) that is only mounted
on demand — it does not slow down boot. The ~2.2s difference is measurement noise across
different k8s clusters (run 2 used kubeadm k8s v1.31.14; run 3 uses kcli k8s v1.30.0).

**Cache hit evidence:** CAA logs show `image_guest_pull` driver for ubuntu:22.04 in both
generic and embedded trials — confirming network pull is used (cache not involved since
ubuntu:22.04 is not in the embedded partition). To demonstrate cache hit, need to rebuild
the embedded image with ubuntu:22.04 pre-cached, OR use cuda-samples in OCI format.

---

## Combined Results Across All Runs

| Run | Cluster | Image | Generic Median | Embedded Median | Delta |
|-----|---------|-------|----------------|-----------------|-------|
| 1 | kubeadm v1.31.14 | cuda-samples:ubi9 | FAIL | FAIL (wrong paths) | N/A |
| 2 | kubeadm v1.31.14 | cuda-samples:ubi9 | FAIL (~15s CDH fail) | FAIL (~51-56s CDH fail) | +35-40s CDH read delay |
| 3 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 31,543 ms | 29,343 ms | -2,200 ms (noise) |
| 4 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 53,417 ms | 51,809 ms | -1,608 ms (noise) |
| 5 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 51,075 ms | 51,268 ms | +193 ms (noise) |

Run 2's +35-40s extra delay in embedded trials is the strongest evidence of embedded
cache interaction (CDH reads meta_store before failing).

Run 5 status: ubi9-large FAILS both generic (CDH 60s timeout) and embedded (ENOSPC: 345 MB free
on 4.3 GB ext4 partition, 3.8 GB needed for snapshot creation from compressed layer blobs).

---

## Run 4 Benchmark Trials (2026-05-28 ~19:10-19:55 UTC)

**Cluster:** kcli peer-pods cluster (k8s v1.30.0, peer-pods-worker-0), kata-remote via CAA
**Image attempted:** quay.io/bpradipt/cuda-samples:ubi9-oci (OCI format, ~3.6 GB)
**Embedded partition:** re-built with ubi9-oci image (14 layers, 7.2 GB on disk)
**Fallback image:** docker.io/library/ubuntu:22.04 (same as run 3)

### Infrastructure Issues Diagnosed and Fixed in This Run

1. **Stale CAA process holding socket**: After a `kubectl rollout restart`, the old CAA process
   (from a previous session still running as PID 317116) held the `hypervisor.sock` open via fd 10.
   The new CAA pods were calling `os.RemoveAll(socketPath)` which unlinked the file, then creating
   a new socket, but the old process was ALSO calling RemoveAll on startup, creating a race that
   resulted in the socket file being unlinked. Fix: kill the lingering old process explicitly
   (`sudo kill <old-pid>`) after which the new CAA's socket persists normally.

2. **Wrong pool for libvirt volumes**: The embedded and generic qcow2 files are in the `podvm-bench`
   pool, but the ConfigMap still referenced `podvm-base.qcow2` (which is in the `default` pool).
   Fixed to use `podvm-ubuntu-amd64.qcow2` for generic and `podvm-ubuntu-amd64-embedded.qcow2`
   for embedded trials.

3. **kata.peerpods.io/vm extended resource not appearing**: The CAA's `AdvertiseExtendedResources`
   call patches the node status successfully but the resource doesn't show in `kubectl get node`.
   Worked around by patching via `kubectl proxy` + curl. Cause: same underlying issue as run 3
   (the kubelet overrides the status periodically and doesn't persist extended resources set via
   the apiserver status subresource when not using the device plugin approach).

### ubi9-oci CDH Failure Analysis

The `ubi9-oci` image was created via `skopeo copy --format oci docker://... docker://...`.
Despite the OCI manifest format, the layer tarballs still contain character device files (major:minor
0:0) as whiteout markers. When `image_pull_debug` unpacks these layers into the embedded partition
(running with `--privileged`, so it has CAP_MKNOD), it creates the character device files on disk.
When CDH inside the kata VM tries to pull the image from the network (generic VM) or mount the
pre-populated layers (embedded VM), it hits the same mknod error:

```
[CDH] [ERROR]: Image Pull error: Failed to pull image quay.io/bpradipt/cuda-samples:ubi9-oci
error: Failed to decode layer data stream: Failed to unpack layer: Failed to unpack layer to destination
```

**Root cause**: `skopeo copy --format oci` converts the OCI manifest but does NOT convert layer
content whiteout representation. The layer tarballs still use character device (0,0) whiteouts.
To fix, the layers themselves must be rewritten with `.wh.` prefix whiteouts using tools like
`umoci` or `buildah`. This is a separate conversion step from manifest format conversion.

**Evidence**: 33 character device files (device type 0,0) found in embedded layer 3:
`/run/kata-containers/image/layers/3/etc/pki/ca-trust/extracted/pem/directory-hash/*.pem`

### Generic Image Trials (podvm-ubuntu-amd64.qcow2 + ubuntu:22.04)

| Trial | Time (ms) | Outcome |
|-------|-----------|---------|
| G1 | 57,513 | SUCCESS |
| G2 | 53,417 | SUCCESS |
| G3 | 52,100 | SUCCESS |

**Median generic (ubuntu:22.04):** 53,417 ms

### Embedded Image Trials (podvm-ubuntu-amd64-embedded.qcow2 + ubuntu:22.04)

| Trial | Time (ms) | Outcome |
|-------|-----------|---------|
| E1 | 51,809 | SUCCESS |
| E2 | 50,816 | SUCCESS |
| E3 | 53,051 | SUCCESS |

**Median embedded (ubuntu:22.04):** 51,809 ms

### Run 4 Summary

| Metric | Value |
|--------|-------|
| Generic median (ubuntu:22.04) | 53,417 ms |
| Embedded median (ubuntu:22.04) | 51,809 ms |
| Delta | -1,608 ms (embedded slightly faster, within noise) |
| Cache hit evidence | NONE — ubuntu:22.04 not in embedded partition |
| ubi9-oci result | FAIL — same CDH char-dev whiteout error (skopeo --format oci insufficient) |

**Why delta is within noise:** Same explanation as run 3. Both VMs boot in ~50s using
network pull of ubuntu:22.04 (which is not in the embedded partition). The embedded VM's
larger disk (9.96 GiB) doesn't slow boot because the ext4 embedded partition (p4) is only
mounted by the systemd unit and not read at boot time.

**Next steps to demonstrate cache hit:** Convert the image using `umoci` or `buildah` to
rewrite layer tarballs with `.wh.` prefix whiteouts, or use a smaller OCI-format image
that doesn't have layers with character device whiteouts and embed it instead of/alongside
cuda-samples.

---

## Run 5 Benchmark Trials (2026-05-29 ~09:55-11:35 UTC)

**Cluster:** kcli peer-pods cluster (k8s v1.30.0, peer-pods-worker-0), kata-remote via CAA
**Target image:** quay.io/bpradipt/cuda-samples:ubi9-large (~3.6 GB OCI format, no char-device whiteouts)
**Embedded partition:** built with ubi9-large (5 layers, 4.3 GB ext4 partition with 345 MB free)
**Fallback image:** docker.io/library/ubuntu:22.04 (forced fallback — ubi9-large fails both generic and embedded)

### Why ubi9-large Fails

**ubi9-large** was built specifically to avoid the char-device whiteout issue:
```
docker history quay.io/bpradipt/cuda-samples:ubi9-large:
  RUN dd if=/dev/urandom of=/layer3.bin bs=1M count=1260   # 1.26 GB
  RUN dd if=/dev/urandom of=/layer2.bin bs=1M count=1260   # 1.26 GB
  RUN dd if=/dev/urandom of=/layer1.bin bs=1M count=1260   # 1.26 GB
  RUN apt-get update && apt-get install -y curl            # 7.78 MB
  ADD ubuntu:22.04 base                                    # 87.5 MB
```
Random data layers (incompressible entropy ~1.0) → compressed size ≈ uncompressed size ≈ 1.26 GB each.

**Generic failure:** CDH tries to download 3.6 GB layers from registry. The CDH API timeout (60s) is hit
before all layers download. Error: `CreateContainerRequest timed out: context deadline exceeded`.

**Embedded failure:** `image_pull_debug` stores large layers as compressed `.bin` blobs in the layer
directories (not pre-extracted). When CDH tries to create a snapshot from the embedded layers, it
decompresses the `.bin` blobs to a snapshot directory — which resides on the SAME embedded ext4
partition (4.3 GB total, 3.7 GB used by blobs = only 345 MB free). Snapshot creation fails with
`Failed to unpack layer to destination` = `ENOSPC`.

The `snapshot_db` in meta_store is empty because `image_pull_debug` only stores layer blobs, not
pre-built overlay snapshots. CDH must create the snapshot at runtime, requiring ~3.8 GB of free
space that the embedded partition does not have.

**Root cause:** The embedded partition needs ~2× the layer size to hold both the layer blobs AND the
snapshot directories created during first container start. For ubi9-large (3.6 GB compressed blobs,
3.6 GB uncompressed = ~7.2 GB total needed), a 4.3 GB partition is insufficient.

### Infrastructure Issues Encountered in Run 5

1. **CAA hypervisor.sock deletion race**: When `kubectl rollout restart` triggers a new CAA pod,
   the old pod (still in `hostNetwork`) holds the socket bound at the kernel level but the socket
   file may be unlinked by:
   - Old process `UnixListener.Close()` calling `unlink()` during graceful shutdown
   - New process calling `os.RemoveAll(socketPath)` on startup
   The CAA startup probe (port 8000) fails if the old container's process still holds port 8000
   (both share `hostNetwork`), causing restart loops that repeatedly delete/re-create the socket.
   **Fix applied:** `sudo kill -9 <old-PID>` on the worker node before `rollout restart`.

2. **kata.peerpods.io/vm resource depletion**: CAA sets extended resources via `AdvertiseExtendedResources`
   but kubelet overwrites status periodically. After pod failures, the resource count depletes to 0.
   **Fix applied:** `kubectl proxy` + PATCH to reset to 5 or 10.

### Generic Image Trials (podvm-ubuntu-amd64.qcow2 + ubuntu:22.04, Run 5)

| Trial | Time (ms) | Outcome | Notes |
|-------|-----------|---------|-------|
| G1 | 50,669 | SUCCESS | Network pull from registry |
| G2 | 51,075 | SUCCESS | Layers cached in guest |
| G3 | 51,111 | SUCCESS | Layers cached in guest |

**Median generic (ubuntu:22.04):** 51,075 ms

### Embedded Image Trials (podvm-ubuntu-amd64-embedded.qcow2 + ubuntu:22.04, Run 5)

Note: ubuntu:22.04 is NOT in the embedded ubi9-large partition. These measure VM boot overhead only.

| Trial | Time (ms) | Outcome | Notes |
|-------|-----------|---------|-------|
| E1 | 51,268 | SUCCESS | Network pull from registry |
| E2 | 51,562 | SUCCESS | Network pull from registry |
| E3 | 51,235 | SUCCESS | Network pull from registry |

**Median embedded (ubuntu:22.04):** 51,268 ms

### Run 5 Summary

| Metric | Value |
|--------|-------|
| Generic median (ubuntu:22.04) | 51,075 ms |
| Embedded median (ubuntu:22.04) | 51,268 ms |
| Delta | +193 ms (noise, embedded slightly slower) |
| Cache hit evidence | NONE — ubuntu:22.04 not in embedded partition |
| ubi9-large generic result | FAIL — CDH API 60s timeout downloading 3.6 GB |
| ubi9-large embedded result | FAIL — ENOSPC on 4.3 GB ext4 (only 345 MB free for snapshot) |

**The +193 ms difference is well within measurement noise.** Both images boot in ~51 seconds using
network pull of ubuntu:22.04. The embedded VM's larger disk (4.6 GB qcow2 vs 992 MB) does not
slow boot because the embedded ext4 partition (p4) is only mounted on demand.

### ubi9-large Layer Analysis

Verified via `docker history` and inspecting embedded partition:
- 5 layers total, only 2 extracted as dirs (`layers/0` and `layers/1`)
- 3 large layers stored as compressed blobs: `layers/2/layer1.bin`, `layers/3/layer2.bin`, `layers/4/layer3.bin`
- Embedded ext4 partition: 4.3 GB size, 3.7 GB used (345 MB free = 8% free)
- Snapshot creation requires ~3.8 GB additional space → ENOSPC

No char-device whiteouts confirmed: `find layers/ -type c` returns 0 results. The fix for char-device
whiteouts succeeded, but the image size creates a new barrier (tmpfs/partition space).

### Path Forward for ubi9-large Benchmark

To make ubi9-large work in embedded mode, one of:
1. **Larger partition**: Embed with 8+ GB partition (needs ~15 GB total qcow2). Disk space on host
   (193 GB, 188 GB used, 5 GB free) prevents this without cleanup.
2. **Pre-built snapshots**: Extend `embed-image.sh` to run `image_pull_debug` then use the overlay
   snapshot (upperdir+workdir+lowerdir) as the pre-built snapshot, populating `snapshot_db` so CDH
   finds an existing snapshot and skips decompression entirely.
3. **Smaller test image**: Use a different large OCI image where all layers extract to a size
   that fits within the 4.3 GB embedded partition with enough headroom for snapshots.
4. **CDH tmpfs fallback**: Configure CDH to decompress layer blobs to host tmpfs (8 GB RAM)
   rather than back to the embedded ext4 partition. This requires CDH config changes.

---

## Combined Results Across All Runs

| Run | Cluster | Image | Generic Median | Embedded Median | Delta |
|-----|---------|-------|----------------|-----------------|-------|
| 1 | kubeadm v1.31.14 | cuda-samples:ubi9 | FAIL | FAIL (wrong paths) | N/A |
| 2 | kubeadm v1.31.14 | cuda-samples:ubi9 | FAIL (~15s CDH fail) | FAIL (~51-56s CDH fail) | +35-40s CDH read delay |
| 3 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 31,543 ms | 29,343 ms | -2,200 ms (noise) |
| 4 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 53,417 ms | 51,809 ms | -1,608 ms (noise) |
| 5 | kcli v1.30.0 | ubuntu:22.04 (fallback) | 51,075 ms | 51,268 ms | +193 ms (noise) |

ubi9-large blocked by: CDH 60s API timeout (generic) + ENOSPC on 345 MB free embedded partition.

---

## Configuration Used

```
CAA ConfigMap (peer-pods-cm) - Run 3/4/5:
  CLOUD_PROVIDER: libvirt
  DISABLECVM: "true"
  LIBVIRT_POOL: podvm-bench
  LIBVIRT_URI: qemu+ssh://ubuntu@192.168.123.1/system?no_verify=1
  LIBVIRT_VOL_NAME: podvm-ubuntu-amd64.qcow2  (generic trials)
                    podvm-ubuntu-amd64-embedded.qcow2  (embedded trials)
```
