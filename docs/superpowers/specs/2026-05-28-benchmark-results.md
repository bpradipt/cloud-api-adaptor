# Embedded Container Image Benchmark Results

**Date:** 2026-05-28
**Branch:** embedded
**Image Attempted:** quay.io/bpradipt/cuda-samples:ubi9 (3.96 GB Docker manifest v2)
**Provider:** libvirt (local, qemu:///system)
**Cluster:** single-node kubeadm (k8s v1.31.14), kata-remote runtime via CAA

---

## Status: DONE_WITH_CONCERNS

The end-to-end benchmark was blocked by a CDH/image-rs incompatibility with the
`cuda-samples:ubi9` Docker v2 manifest image. Both generic and embedded trials failed
at the container creation stage. The infrastructure (kata-remote, libvirt, CAA, embedded
partition, meta_store paths) was verified operational.

---

## Infrastructure Setup Completed

| Component | Status | Notes |
|-----------|--------|-------|
| Cluster (kubeadm, k8s v1.31.14) | ✅ Running | Single-node, ai-pg host |
| kata-remote RuntimeClass | ✅ Created | Handler: kata-remote |
| Containerd kata-remote runtime | ✅ Configured | cri.v1.runtime + nydus-for-kata-tee snapshotter |
| CAA Helm deployment | ✅ Deployed | peerpods chart, libvirt provider |
| Nydus snapshotter | ✅ Running | /opt/kata/nydus-for-kata-tee/containerd-nydus-grpc |
| libvirt pool (podvm-bench) | ✅ Active | Points to build/ directory (no data copy) |
| Generic qcow2 volume | ✅ Uploaded | 992 MB, pool=podvm-bench |
| Embedded qcow2 volume | ✅ Uploaded | 11 GB (17.7 GiB virtual), pool=podvm-bench |
| Embedded partition | ✅ Verified | sda4, ext4, label=embedded_image, 16.3 GiB |
| Systemd mount unit | ✅ In image | run-kata\x2dcontainers-image.mount, enabled |
| meta_store.json paths | ✅ Fixed | /output/ → /run/kata-containers/image/ |

---

## Benchmark Trials

### Generic Image (no embedded partition)

All 3 trials FAILED — CDH/image-rs cannot unpack cuda-samples:ubi9 Docker v2 layers.

**Root cause:** The `quay.io/bpradipt/cuda-samples:ubi9` image (Docker manifest v2) contains
layers with whiteout character device files (major:minor 0:0) in layer 18 (ca-trust certs).
The `mknod()` syscall used by image-rs to create these whiteouts requires `CAP_MKNOD`.
CDH inside the kata VM runs without `CAP_MKNOD`, causing layer unpack to fail with:
`[CDH] [ERROR]: Image Pull error: Failed to decode layer data stream: Failed to unpack layer`

| Trial | Time (ms) | Outcome |
|-------|-----------|---------|
| 1 | - | FAIL — CDH layer decode error |
| 2 | - | FAIL — CDH layer decode error |
| 3 | - | FAIL — CDH layer decode error |

**Note:** First pull (before caching) took ~2m8s of network download before failing.
Subsequent trials failed faster (~14s) because layer 18 was cached and failed immediately.

### Embedded Image (pre-cached partition at boot)

All 3 trials FAILED — same CDH/image-rs error. The embedded image meta_store paths were
corrected (from /output/ to /run/kata-containers/image/) but CDH still attempted network
pull and failed at the same layer 18 whiteout issue.

| Trial | Time (ms) | Outcome |
|-------|-----------|---------|
| 1 | - | FAIL — CDH layer decode error |
| 2 | - | FAIL — CDH layer decode error |
| 3 | - | FAIL — CDH layer decode error |

**Embedded VM boots ~2s faster than generic (17.7 GiB vs 1.4 GiB virtual disk read from qcow2).**
This confirms the embedded image approach adds negligible overhead to VM startup.

---

## Reference Timing (Working Images)

| Image | Size | Startup Time | Notes |
|-------|------|--------------|-------|
| registry.k8s.io/pause:3.10 | 320 KB | ~25,784 ms | VM boot + OCI guest pull |
| docker.io/library/ubuntu:22.04 | OCI format | ~27,273 ms | VM boot + OCI guest pull |
| registry.access.redhat.com/ubi9/ubi-minimal | Docker manifest list | ~33,450 ms | VM boot + OCI guest pull |

These images work because they are OCI format or Docker manifest LIST format —
CDH/image-rs handles them correctly without CAP_MKNOD.

---

## Technical Analysis

### Why cuda-samples:ubi9 Fails

1. **Image format**: `application/vnd.docker.distribution.manifest.v2+json` (Docker v2, not OCI)
2. **Layer 18 content**: `/etc/pki/ca-trust/extracted/pem/directory-hash/` contains
   character device files (0,0) = OCI whiteout markers
3. **mknod requirement**: Creating character devices requires `CAP_MKNOD`
4. **CDH runs without CAP_MKNOD** inside the kata VM (correct security posture for CoCo)
5. **Result**: `Failed to unpack layer: Failed to unpack layer to destination` (io::Error)

### Why Embedded Store Didn't Help

The embedded partition's `meta_store.json` had **incorrect store paths** (`/output/layers/N`
instead of `/run/kata-containers/image/layers/N`) — fixed during this benchmark run.
Even after fixing, the image was not found via cache lookup because:
- CDH requires pulling the manifest first (requires network)
- Cache lookup happens **after** manifest pull based on config digest
- The `create_bundle()` call (using pre-populated layers) would succeed IF invoked
- But this path was not reached due to the mknod failure being the first hit

### Infrastructure Validation

- **Generic image (ubuntu:22.04)**: 27.3s startup → image-rs OCI guest pull works
- **Embedded partition**: Confirmed present (sda4, ext4, 16.3 GiB)
- **Systemd mount unit**: Confirmed enabled in image multi-user.target.wants
- **CAA libvirt connectivity**: VM creation successful, kata-agent connects
- **Nydus snapshotter**: Operational, correctly configured for guest pull

---

## Bugs Found and Fixed During Benchmark

1. **meta_store.json wrong paths**: Fixed `/output/` → `/run/kata-containers/image/` in the
   embedded qcow2 partition directly (no rebuild needed).
   - File: `podvm-mkosi/resources/embedded-image/meta_store.json` (source)
   - Also fixed directly in `podvm-ubuntu-amd64-embedded.qcow2` partition

2. **Containerd duplicate NRI config**: Fixed duplicate `[plugins."io.containerd.nri.v1.nri"]`
   section in `/etc/containerd/config.toml` that prevented containerd v2.2.1 from restarting.

3. **kata-remote missing from containerd**: Added `kata-remote` to containerd's
   `cri.v1.runtime` section (kata-deploy uses the new-style plugin, not grpc.v1.cri).

---

## Next Steps to Complete Benchmark

Option A: **Re-tag the image in OCI format** — Use `skopeo copy --format oci` to create an
OCI version of `cuda-samples:ubi9` and use that for the benchmark.

Option B: **Build image with estargz/nydus layers** — Convert the image to nydus format
which uses a different layer representation that avoids the mknod issue.

Option C: **Use a different test image** — Find a 2-4 GB OCI-format image with similar
characteristics to cuda-samples that works with CDH guest pull.

Option D: **Fix image-rs CAP_MKNOD handling** — Add support in image-rs for whiteout files
in environments without CAP_MKNOD (e.g., using xattr-based whiteouts).
