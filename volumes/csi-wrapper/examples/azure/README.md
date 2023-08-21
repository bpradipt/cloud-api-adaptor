# Azure File CSI Wrapper for Peer Pod Storage

## Peer Pod example using CSI Wrapper with azurefiles-csi-driver on OpenShift

Note: This approach is not supported for production. Use this for trying out peer-pods with
persistent storage volumes on OpenShift

Prerequisites
- Ensure the OpenShift cluster setup on Azure includes the Azure File CSI driver. Typically it
is installed by default
- Ensure OSC with peer-pods is configured on the cluster

### Switch the Azure File CSI driver to unmanaged mode

- Edit the `clustercsidrivers.operator.openshift.io` CR named `file.csi.azure.com` and change
the `managementState` to `Unmanaged`

```bash
oc patch clustercsidriver file.csi.azure.com --type=merge -p "{\"spec\":{\"managementState\":\"Unmanaged\"}}"
```

### Reconfigure the Azure File CSI driver to use only the nodes with OSC/peer-pods configured

- Ensure that the Azure File CSI driver is only setup for the OSC/peer-pods nodes. By default the Azure File CSI
driver is installed on all the cluster nodes. You'll need to edit the CSI driver node daemonset to run only on
the nodes which has Kata/peer-pods configured.

```bash
oc patch -n openshift-cluster-csi-drivers ds azure-file-csi-driver-node -p '{"spec":{"template":{"spec":{"nodeSelector": { "node-role.kubernetes.io/kata-oc":""}}}}}'
```

### Deploy csi-wrapper to patch Azure File CSI driver

1. Switch to the `cloud-api-adaptor` source directory
```bash
cd ~/cloud-api-adaptor
```

2. Create the PeerpodVolume CRD object
```bash
oc apply -f volumes/csi-wrapper/crd/peerpodvolume.yaml
```

The output looks like:
```bash
customresourcedefinition.apiextensions.k8s.io/peerpodvolumes.confidentialcontainers.org created
```

3. Configure RBAC so that the wrapper has access to the required operations
```bash
oc apply -f volumes/csi-wrapper/examples/azure/azure-files-csi-wrapper-runner.yaml
oc apply -f volumes/csi-wrapper/examples/azure/azure-files-csi-wrapper-podvm.yaml
```

4. Patch the Azure File CSI driver:
```bash
oc patch -n openshift-cluster-csi-drivers deploy azure-file-csi-driver-controller --patch-file volumes/csi-wrapper/examples/azure/patch-controller.yaml

oc patch -n openshift-cluster-csi-drivers ds azure-file-csi-driver-node --patch-file volumes/csi-wrapper/examples/azure/patch-node.yaml

```

5. Create **storage class**:
```bash
oc apply -f volumes/csi-wrapper/examples/azure/azure-file-StorageClass-for-peerpod.yaml
```

### Run a sample workload for verification

1. Create one pvc that uses the Azure File CSI driver
```bash
oc apply -f volumes/csi-wrapper/examples/azure/my-pvc.yaml
```

2. Wait for the pvc status to become `bound`
```bash
$ k get pvc
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS         AGE
pvc-azurefile   Bound    pvc-3edc7a93-4531-4034-8818-1b1608907494   1Gi        RWO            azure-file-storage   3m11s
```

3. Create the nginx peer-pod demo with with `podvm-wrapper` and `azurefile-csi-driver` containers
```bash
oc apply -f volumes/csi-wrapper/examples/azure/nginx-kata-with-my-pvc-and-csi-wrapper.yaml
```

4. Exec into the container and check the mount

```bash
oc exec nginx-pv -c nginx -i -t -- sh
# mount | grep mount-path
```
