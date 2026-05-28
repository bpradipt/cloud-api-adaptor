# Embedded Container Image in PodVM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed a pre-unpacked OCI container image (cuda-samples:ubi9, 3.6 GB) into a dedicated ext4 partition appended to the podvm qcow2, so that image-rs finds it cached at `/run/kata-containers/image` on boot — eliminating the pull from pod startup time.

**Architecture:** A post-processing shell script appends a new GPT ext4 partition (labeled `embedded_image`) to `system.raw` after the normal mkosi build, pre-populated with the image-rs layer store unpacked by `image_pull_debug`. A conditional systemd mount unit present in all images mounts this partition at `/run/kata-containers/image` before kata-agent starts; when the partition is absent the unit is a no-op, leaving generic images unaffected.

**Tech Stack:** bash, sfdisk, losetup, mkfs.ext4, rsync, qemu-img, systemd mount units, mkosi skeleton trees, GNU make, Docker (for the download step).

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/run-kata\x2dcontainers-image.mount` | **Create** | Systemd mount unit that mounts the embedded_image partition at `/run/kata-containers/image` |
| `src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system-preset/30-coco.preset` | **Modify** | Enable the new mount unit in all image variants |
| `src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh` | **Create** | Post-processing script: appends ext4 partition to system.raw and converts to qcow2 |
| `src/cloud-api-adaptor/podvm-mkosi/Makefile` | **Modify** | Add `download-embedded-image` and `image-embedded` targets |
| `src/cloud-api-adaptor/podvm-mkosi/.gitignore` | **Modify** | Exclude `resources/embedded-image/` (3.6 GB, never committed) |

---

## Task 1: Systemd mount unit

**Files:**
- Create: `src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/run-kata\x2dcontainers-image.mount`
- Modify: `src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system-preset/30-coco.preset`

**Context:** The unit file name encodes the mount path `/run/kata-containers/image`:
slashes become `-`, and literal `-` inside a path segment becomes `\x2d`. So
`/run/kata-containers/image` → `run-kata\x2dcontainers-image.mount`. This is the literal
filename on disk (the four characters `\x2d` are not a hex escape — they are literal backslash,
x, 2, d).

The unit uses `ConditionPathExists=/dev/disk/by-label/embedded_image`: when the partition is
absent (generic image), systemd marks the unit as successfully skipped. `Before=kata-agent.service`
still fires in that case (the skip counts as complete), so kata-agent ordering is preserved.

systemd mount units automatically create the `Where=` directory tree (equivalent to `mkdir -p`),
so `/run/kata-containers` and `/run/kata-containers/image` are created automatically before mount.

- [ ] **Step 1.1: Create the mount unit file**

  The filename has literal `\x2d` in it. Use printf or a quoted path to create it:

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system
  ```

  Create file `run-kata\x2dcontainers-image.mount` with content:

  ```ini
  [Unit]
  Description=Mount embedded container image store
  Documentation=https://github.com/confidential-containers/cloud-api-adaptor
  ConditionPathExists=/dev/disk/by-label/embedded_image
  After=systemd-repart.service local-fs.target
  Before=kata-agent.service

  [Mount]
  What=/dev/disk/by-label/embedded_image
  Where=/run/kata-containers/image
  Type=ext4
  Options=rw,relatime

  [Install]
  WantedBy=multi-user.target
  ```

  The `ConditionPathExists` check uses the `/dev/disk/by-label/` symlink which udev creates at
  boot. `After=local-fs.target` ensures udev has settled before we look for the device.

- [ ] **Step 1.2: Verify the filename is correct**

  ```bash
  ls "src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/"
  ```

  Expected: you see `run-kata\x2dcontainers-image.mount` listed (15 characters between `run-` and `.mount`: `kata\x2dcontainers-image`).

  ```bash
  systemd-analyze verify \
    "src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/run-kata\x2dcontainers-image.mount" \
    2>&1 || true
  ```

  Expected: no errors (warnings about missing units in this environment are acceptable).

- [ ] **Step 1.3: Enable the unit in the preset**

  Open `src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system-preset/30-coco.preset` and append one line at the end:

  ```
  enable run-kata\x2dcontainers-image.mount
  ```

  Full file after edit:

  ```
  enable agent-protocol-forwarder.path
  enable attestation-agent.path
  enable api-server-rest.path
  enable confidential-data-hub.path
  enable kata-agent.path
  enable netns@.service
  enable setup-nat-for-imds.service

  enable gen-issue.service
  enable image-env.service
  enable scratch-storage.path

  enable run-kata\x2dcontainers-image.mount
  ```

- [ ] **Step 1.4: Commit**

  ```bash
  cd /home/ubuntu/cloud-api-adaptor
  git add \
    "src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system/run-kata\x2dcontainers-image.mount" \
    src/cloud-api-adaptor/podvm-mkosi/mkosi.images/system/mkosi.skeleton/usr/lib/systemd/system-preset/30-coco.preset
  git commit -m "podvm: add conditional systemd mount unit for embedded container image store

  Add run-kata\x2dcontainers-image.mount that mounts the embedded_image ext4 partition
  at /run/kata-containers/image before kata-agent starts. ConditionPathExists makes it
  a no-op on generic images where the partition is absent."
  ```

---

## Task 2: embed-image.sh post-processing script

**Files:**
- Create: `src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh`

**Context:** This script takes the mkosi-produced `system.raw` and a pre-populated image store
directory, appends a new GPT ext4 partition to a copy of the raw image, formats it, copies the
image store into it, and converts to qcow2. It requires root because it uses loop devices.

The script works on a copy (`system-embedded.raw`) so `system.raw` stays pristine for the generic
qcow2 that `make image` already produced. The copy is removed after the qcow2 is produced.

`sfdisk --append` adds a partition using all remaining free space after extending the image.
`losetup -P` makes the kernel re-read the partition table of the loop device.

- [ ] **Step 2.1: Create the hack/ directory and script**

  ```bash
  mkdir -p src/cloud-api-adaptor/podvm-mkosi/hack
  ```

  Create `src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh`:

  ```bash
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
  echo "Copying $RAW_IMAGE → $WORK_IMAGE ..."
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

  # Discover the new (last) partition
  PART_NUM=$(lsblk -no NAME "$LOOP_DEV" | grep -c "^$(basename "$LOOP_DEV")p")
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
  ```

- [ ] **Step 2.2: Make the script executable**

  ```bash
  chmod +x src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh
  ```

- [ ] **Step 2.3: Smoke-test the script with a tiny synthetic image**

  This test creates a minimal GPT disk, runs the script on it, and verifies the output.
  Run as root or with sudo:

  ```bash
  cd /tmp

  # Create a 200 MB minimal GPT raw image (simulates system.raw)
  dd if=/dev/zero of=smoke-test.raw bs=1M count=200
  parted -s smoke-test.raw mklabel gpt
  parted -s smoke-test.raw mkpart primary ext4 1MiB 199MiB

  # Create a tiny fake image store
  mkdir -p fake-store/layers
  echo "layer-data" > fake-store/layers/layer0.txt

  # Run the script
  sudo /home/ubuntu/cloud-api-adaptor/src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh \
      smoke-test.raw fake-store smoke-test-embedded.qcow2
  ```

  Expected output ends with:
  ```
  Done: smoke-test-embedded.qcow2
  ```

- [ ] **Step 2.4: Verify the output partition and content**

  ```bash
  cd /tmp

  # Inspect partitions in the output qcow2
  qemu-img convert -f qcow2 -O raw smoke-test-embedded.qcow2 verify.raw
  sfdisk --list verify.raw
  ```

  Expected: output shows 2 partitions, the second labeled `embedded_image`.

  ```bash
  # Mount the embedded_image partition and check content
  LOOP=$(sudo losetup -f --show -P verify.raw)
  sudo mkdir -p /mnt/verify
  sudo mount ${LOOP}p2 /mnt/verify
  cat /mnt/verify/layers/layer0.txt
  ```

  Expected output: `layer-data`

  ```bash
  # Clean up
  sudo umount /mnt/verify
  sudo losetup -d "$LOOP"
  rm -f smoke-test.raw smoke-test-embedded.qcow2 verify.raw
  rm -rf fake-store
  ```

- [ ] **Step 2.5: Commit**

  ```bash
  cd /home/ubuntu/cloud-api-adaptor
  git add src/cloud-api-adaptor/podvm-mkosi/hack/embed-image.sh
  git commit -m "podvm: add embed-image.sh to append pre-populated ext4 partition to raw image

  Post-processing script that extends system.raw, appends a GPT ext4 partition
  labeled 'embedded_image', populates it from the image-rs store directory, and
  converts to qcow2. Working on a copy preserves the original system.raw."
  ```

---

## Task 3: Makefile targets and .gitignore

**Files:**
- Modify: `src/cloud-api-adaptor/podvm-mkosi/Makefile`
- Modify: `src/cloud-api-adaptor/podvm-mkosi/.gitignore`

**Context:** Two new targets:
- `download-embedded-image`: runs `image_pull_debug` inside the tools container via `docker run`,
  writing the image-rs store to `resources/embedded-image/`. Idempotent — re-running refreshes
  the store.
- `image-embedded`: depends on `image` (normal mkosi build), then calls `embed-image.sh` with
  sudo. Output is `build/podvm-$(PODVM_DISTRO)-$(DISTRO_ARCH)-embedded.qcow2`.

The `resources/embedded-image/` directory must be gitignored (3.6 GB). The existing `.gitignore`
already has `resources/*` which covers it — but we add an explicit entry to make intent clear.

- [ ] **Step 3.1: Add variables and targets to Makefile**

  In `src/cloud-api-adaptor/podvm-mkosi/Makefile`, add the following block immediately after
  the existing `MKOSI_VERSION ?= v26` line (around line 18):

  ```makefile
  EMBEDDED_IMAGE ?= quay.io/bpradipt/cuda-samples:ubi9
  EMBEDDED_IMAGE_STORE ?= $(shell pwd)/resources/embedded-image
  TOOLS_IMAGE ?= quay.io/bpradipt/kata-initrd-debug-tools:latest
  ```

  Then add the following two targets at the end of the Makefile (after the `clean` target):

  ```makefile
  .PHONY: download-embedded-image
  download-embedded-image:
  	@echo "Downloading and unpacking embedded container image: $(EMBEDDED_IMAGE)"
  	mkdir -p $(EMBEDDED_IMAGE_STORE)
  	docker run --rm \
  		-v $(EMBEDDED_IMAGE_STORE):/output \
  		$(TOOLS_IMAGE) \
  		/tools/image_pull_debug \
  			--image $(EMBEDDED_IMAGE) \
  			--work-dir /output
  	@echo "Image store written to: $(EMBEDDED_IMAGE_STORE)"

  .PHONY: image-embedded
  image-embedded: image
  	@echo "Building embedded image..."
  	@if [ ! -d "$(EMBEDDED_IMAGE_STORE)" ] || [ -z "$$(ls -A $(EMBEDDED_IMAGE_STORE) 2>/dev/null)" ]; then \
  		echo "ERROR: $(EMBEDDED_IMAGE_STORE) is empty. Run 'make download-embedded-image' first."; \
  		exit 1; \
  	fi
  	sudo $(shell pwd)/hack/embed-image.sh \
  		$(shell pwd)/build/system.raw \
  		$(EMBEDDED_IMAGE_STORE) \
  		$(shell pwd)/build/podvm-$(PODVM_DISTRO)-$(DISTRO_ARCH)-embedded.qcow2
  	@echo "Embedded image: build/podvm-$(PODVM_DISTRO)-$(DISTRO_ARCH)-embedded.qcow2"
  ```

  Note: Makefile recipes use **tabs** for indentation, not spaces. Verify after editing.

- [ ] **Step 3.2: Verify Makefile tab indentation**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi
  grep -P "^\t" Makefile | tail -10
  ```

  Expected: lines starting with actual tab characters (not spaces). If any lines under the new
  targets use spaces, fix them.

- [ ] **Step 3.3: Verify the new targets are recognized**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi
  make -n download-embedded-image 2>&1 | head -5
  make -n image-embedded 2>&1 | head -5
  ```

  Expected: `make -n` prints what would run without executing it. You should see `docker run ...`
  for the first and `sudo .../hack/embed-image.sh ...` for the second. No "No rule to make target"
  errors.

- [ ] **Step 3.4: Update .gitignore**

  In `src/cloud-api-adaptor/podvm-mkosi/.gitignore`, the existing `resources/*` line already
  covers `resources/embedded-image/`. Add an explicit comment so intent is clear. Add these lines
  at the end of `.gitignore`:

  ```
  # Embedded container image store (downloaded by 'make download-embedded-image')
  # Already covered by resources/* above, but listed explicitly for clarity
  resources/embedded-image/
  ```

- [ ] **Step 3.5: Commit**

  ```bash
  cd /home/ubuntu/cloud-api-adaptor
  git add \
      src/cloud-api-adaptor/podvm-mkosi/Makefile \
      src/cloud-api-adaptor/podvm-mkosi/.gitignore
  git commit -m "podvm: add download-embedded-image and image-embedded Makefile targets

  - download-embedded-image: pulls and unpacks the target OCI image into
    resources/embedded-image/ using image_pull_debug from the debug tools image
  - image-embedded: runs the normal mkosi build then embed-image.sh to produce
    podvm-<distro>-<arch>-embedded.qcow2 alongside the generic image"
  ```

---

## Task 4: Build and smoke-test the embedded image

**Context:** This task builds both images and verifies the embedded partition is present and
reachable. It does NOT require a running Kubernetes cluster — just libvirt + qemu for the boot
check.

- [ ] **Step 4.1: Download the embedded container image store**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi
  PODVM_DISTRO=ubuntu make download-embedded-image
  ```

  Expected: docker pulls the tools image, `image_pull_debug` downloads and unpacks
  `quay.io/bpradipt/cuda-samples:ubi9`. This takes several minutes. On completion:

  ```bash
  ls -lh resources/embedded-image/
  du -sh resources/embedded-image/
  ```

  Expected: directory exists, non-empty, total size in the GB range.

- [ ] **Step 4.2: Build the normal (generic) podvm image**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi
  PODVM_DISTRO=ubuntu make image
  ```

  Expected: produces `build/podvm-ubuntu-amd64.qcow2` and `build/system.raw`.

  ```bash
  ls -lh build/podvm-ubuntu-amd64.qcow2 build/system.raw
  ```

- [ ] **Step 4.3: Build the embedded podvm image**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi
  PODVM_DISTRO=ubuntu make image-embedded
  ```

  Expected: embed-image.sh runs (requires sudo password if not already cached), produces
  `build/podvm-ubuntu-amd64-embedded.qcow2`. This is larger than the generic image by ~3.6 GB.

  ```bash
  ls -lh build/podvm-ubuntu-amd64.qcow2 build/podvm-ubuntu-amd64-embedded.qcow2
  ```

  Expected: embedded image is significantly larger (several GB larger).

- [ ] **Step 4.4: Verify the embedded partition is present in the output**

  ```bash
  cd src/cloud-api-adaptor/podvm-mkosi

  # Convert embedded qcow2 back to raw temporarily for inspection
  qemu-img convert -f qcow2 -O raw \
      build/podvm-ubuntu-amd64-embedded.qcow2 /tmp/verify-embedded.raw

  # List partitions
  sfdisk --list /tmp/verify-embedded.raw
  ```

  Expected: output shows a partition with `Name` = `embedded_image`.

  ```bash
  # Mount the embedded_image partition and spot-check content
  LOOP=$(sudo losetup -f --show -P /tmp/verify-embedded.raw)
  PART_NUM=$(lsblk -no NAME "$LOOP" | grep -c "^$(basename "$LOOP")p")
  sudo mkdir -p /mnt/embedded-check
  sudo mount "${LOOP}p${PART_NUM}" /mnt/embedded-check
  ls /mnt/embedded-check/
  sudo umount /mnt/embedded-check
  sudo losetup -d "$LOOP"
  rm /tmp/verify-embedded.raw
  ```

  Expected: the mounted partition contains the image-rs store structure (directories like
  `layers`, metadata files). Not empty.

- [ ] **Step 4.5: Verify the generic image does NOT have the partition**

  ```bash
  qemu-img convert -f qcow2 -O raw \
      build/podvm-ubuntu-amd64.qcow2 /tmp/verify-generic.raw
  sfdisk --list /tmp/verify-generic.raw | grep -c embedded_image || echo "0 matches — correct"
  rm /tmp/verify-generic.raw
  ```

  Expected: `0 matches — correct` (no embedded_image partition in the generic image).

- [ ] **Step 4.6: Commit smoke-test results (no code change; just verify CI passes)**

  No file changes in this step. The commit log is your record.

---

## Task 5: Upload images to libvirt and measure startup time

**Context:** Uses the existing libvirt cluster (see memory: kcli peer-pods cluster, KUBECONFIG at
`~/.kcli/clusters/peer-pods/auth/kubeconfig`, libvirt default network 192.168.123.0/24).
Both images are uploaded to libvirt as separate volumes. The CAA libvirt provider selects the
volume by name; we create two separate CAA configuration secrets or annotations to point at each
image, then measure pod startup time.

This task assumes libvirt and the CAA are already running from a previous setup. If not, run
`/libvirt-kcli-caa` first.

- [ ] **Step 5.1: Upload both images to libvirt**

  ```bash
  # Upload generic image
  virsh vol-create-as default podvm-ubuntu-amd64.qcow2 \
      $(stat -c%s src/cloud-api-adaptor/podvm-mkosi/build/podvm-ubuntu-amd64.qcow2) \
      --format qcow2

  virsh vol-upload --pool default podvm-ubuntu-amd64.qcow2 \
      src/cloud-api-adaptor/podvm-mkosi/build/podvm-ubuntu-amd64.qcow2

  # Upload embedded image
  virsh vol-create-as default podvm-ubuntu-amd64-embedded.qcow2 \
      $(stat -c%s src/cloud-api-adaptor/podvm-mkosi/build/podvm-ubuntu-amd64-embedded.qcow2) \
      --format qcow2

  virsh vol-upload --pool default podvm-ubuntu-amd64-embedded.qcow2 \
      src/cloud-api-adaptor/podvm-mkosi/build/podvm-ubuntu-amd64-embedded.qcow2
  ```

  Verify:
  ```bash
  virsh vol-list default | grep podvm-ubuntu
  ```

  Expected: both volumes listed.

- [ ] **Step 5.2: Confirm the mechanism for switching the volume name between runs**

  The CAA libvirt provider reads `LIBVIRT_VOL_NAME` (default: `podvm-base.qcow2`) to select the
  podvm disk. This env var is set in the provider ConfigMap deployed by the helm chart.

  Find the ConfigMap:
  ```bash
  export KUBECONFIG=~/.kcli/clusters/peer-pods/auth/kubeconfig
  kubectl get cm -n confidential-containers-system | grep peer-pods
  ```

  To switch volume names between the generic and embedded runs, patch the ConfigMap directly:
  ```bash
  # Switch to embedded image
  kubectl patch cm -n confidential-containers-system peer-pods-cm \
      --type merge -p '{"data":{"LIBVIRT_VOL_NAME":"podvm-ubuntu-amd64-embedded.qcow2"}}'

  # Restart the CAA daemonset to pick up the new value
  kubectl rollout restart daemonset/cloud-api-adaptor -n confidential-containers-system
  kubectl rollout status daemonset/cloud-api-adaptor -n confidential-containers-system
  ```

  To switch back to generic:
  ```bash
  kubectl patch cm -n confidential-containers-system peer-pods-cm \
      --type merge -p '{"data":{"LIBVIRT_VOL_NAME":"podvm-ubuntu-amd64.qcow2"}}'
  kubectl rollout restart daemonset/cloud-api-adaptor -n confidential-containers-system
  kubectl rollout status daemonset/cloud-api-adaptor -n confidential-containers-system
  ```

  Note: the ConfigMap name (`peer-pods-cm`) may differ; use the name from the `kubectl get cm`
  output above if it is different.

- [ ] **Step 5.3: Deploy a test pod using the generic image — record startup time**

  Update libvirt.properties (or the CAA secret) to point at `podvm-ubuntu-amd64.qcow2`, then
  re-apply the CAA configuration:

  ```bash
  export KUBECONFIG=~/.kcli/clusters/peer-pods/auth/kubeconfig

  # Record start time
  START=$(date +%s%N)

  kubectl apply -f - <<'EOF'
  apiVersion: v1
  kind: Pod
  metadata:
    name: cuda-generic
    annotations:
      io.containerd.cri.runtime-handler: kata-remote
  spec:
    runtimeClassName: kata-remote
    containers:
    - name: cuda
      image: quay.io/bpradipt/cuda-samples:ubi9
      command: ["/bin/sh", "-c", "sleep 3600"]
  EOF

  # Poll until Running
  kubectl wait --for=condition=Ready pod/cuda-generic --timeout=600s
  END=$(date +%s%N)
  echo "Generic startup: $(( (END - START) / 1000000 )) ms"
  ```

  Record the time. Delete the pod before the next step:

  ```bash
  kubectl delete pod cuda-generic
  ```

- [ ] **Step 5.4: Deploy a test pod using the embedded image — record startup time**

  Update libvirt.properties to point at `podvm-ubuntu-amd64-embedded.qcow2`, re-apply the
  CAA configuration, then repeat the timing:

  ```bash
  export KUBECONFIG=~/.kcli/clusters/peer-pods/auth/kubeconfig

  START=$(date +%s%N)

  kubectl apply -f - <<'EOF'
  apiVersion: v1
  kind: Pod
  metadata:
    name: cuda-embedded
    annotations:
      io.containerd.cri.runtime-handler: kata-remote
  spec:
    runtimeClassName: kata-remote
    containers:
    - name: cuda
      image: quay.io/bpradipt/cuda-samples:ubi9
      command: ["/bin/sh", "-c", "sleep 3600"]
  EOF

  kubectl wait --for=condition=Ready pod/cuda-embedded --timeout=600s
  END=$(date +%s%N)
  echo "Embedded startup: $(( (END - START) / 1000000 )) ms"
  ```

- [ ] **Step 5.5: Verify the mount is active inside the embedded podvm**

  While the embedded pod is running, SSH into the podvm (or use `kubectl exec`) and confirm
  the partition is mounted correctly:

  ```bash
  # Find the VM's IP from libvirt
  virsh domifaddr --source agent $(virsh list --name | grep -v '^$' | grep -v peer-pods) 2>/dev/null | head -5

  # Or check via kata-agent logs
  kubectl logs cuda-embedded 2>/dev/null | head -20
  ```

  If SSH access is available (debug image):
  ```bash
  # SSH into the podvm (use the IP from virsh above)
  ssh -o StrictHostKeyChecking=no root@<PODVM_IP>
  mount | grep embedded_image
  ```

  Expected output contains:
  ```
  /dev/vda<N> on /run/kata-containers/image type ext4 (rw,relatime)
  ```

  Where `<N>` is the partition number of the embedded_image partition.

- [ ] **Step 5.6: Run each scenario 3 times and report median**

  Repeat steps 5.3 and 5.4 three times each. Record all times:

  | Run | Generic (ms) | Embedded (ms) |
  |-----|-------------|---------------|
  | 1   |             |               |
  | 2   |             |               |
  | 3   |             |               |
  | Median |         |               |

  The delta (Generic median − Embedded median) is the startup time saved by embedding the image.

- [ ] **Step 5.7: Commit timing results as a markdown note**

  ```bash
  cd /home/ubuntu/cloud-api-adaptor
  git add -A
  git commit -m "test: record embedded image startup time benchmark results"
  ```

---

## Known Limitations

- **Scratch space incompatibility:** If `scratch-storage.service` also tries to mount at
  `/run/kata-containers/image` (when `ENABLE_SCRATCH_SPACE=true`), it conflicts with the embedded
  mount. Do not enable scratch space when using the embedded image variant for testing.
- **Image-specific qcow2:** The embedded qcow2 contains one specific container image. Pods
  requesting a different image will still trigger a pull (image-rs will download to the
  already-mounted partition, which has free headroom).
- **sudo required:** `embed-image.sh` requires root; `make image-embedded` calls it with `sudo`.
