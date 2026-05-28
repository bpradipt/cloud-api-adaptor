# Embedded Container Image Benchmark Results

**Date:** 2026-05-28
**Branch:** embedded
**Image Attempted:** quay.io/bpradipt/cuda-samples:ubi9 (3.96 GB Docker manifest v2)
**Provider:** libvirt (local, qemu:///system)
**Cluster:** single-node kubeadm (k8s v1.31.14), kata-remote runtime via CAA

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

## Configuration Used

```
CAA ConfigMap (peer-pods-cm):
  CLOUD_PROVIDER: libvirt
  DISABLECVM: "true"
  LIBVIRT_POOL: podvm-bench
  LIBVIRT_URI: qemu+ssh://root@192.168.123.1/system?no_verify=1
  LIBVIRT_VOL_NAME: podvm-ubuntu-amd64.qcow2  (generic trials)
                    podvm-ubuntu-amd64-embedded.qcow2  (embedded trials)
```
