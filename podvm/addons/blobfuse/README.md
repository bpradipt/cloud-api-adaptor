## Introduction

This addon enables Azure blobfuse to mount blobs inside pod VM for use by the pod.

Sample configurations are provided here. You'll also need the oci hook.
A sample oci hook for blobfuse can be downloaded by following the instructions below:

```
wget https://github.com/bpradipt/blobfuse-oci-hook/releases/download/v0.0.1/blobfuse-oci-hook-v0.0.1-linux-amd64.tar.gz
tar zxvf blobfuse-oci-hook-v0.0.1-linux-amd64.tar.gz
```
The binary `blobfuse-oci-hook` should be placed under `podvm/addons/blobfuse` directory.

The hook expects a configuration file which is a json. An example configuration file is shown below and is 
available under `podvm/addons/blobfuse`
```
{
  "activation_flag": "HOOK",
  "program_path": "/usr/bin/blobfuse2",
  "host_mountpoint": "/blobdata",
  "container_mountpoint": "/blobdata"
}
```

The value of the `activation_flag` (ie `HOOK` as shown above) needs to be provided as environment variable to the container.
The `container_mountpoint` can also be provided as an environment variable (`CONTAINER_MOUNTPOINT`) in the container using this hook.

Further, the blobfuse auth and other related parameters need to be provided as environment variables. 
Details of the blobfuse environment variables can be found [here](https://github.com/Azure/azure-storage-fuse#environment-variables).

Following is an example pod manifest.
```
apiVersion: v1
kind: Pod
metadata:
  name: test
  labels:
    app: test
spec:
  runtimeClassName: kata-remote
  containers:
    - name: ubuntu
      image: ubuntu
      command: ["sleep"]
      args: ["infinity"]
      env:      
        - name: HOOK
          value: "true"
        - name: AZURE_STORAGE_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: storage-secret
              key: AZURE_STORAGE_ACCESS_KEY
        - name: AZURE_STORAGE_ACCOUNT_CONTAINER
          valueFrom:
            secretKeyRef:
              name: storage-secret
              key: AZURE_STORAGE_ACCOUNT_CONTAINER
        - name: AZURE_STORAGE_AUTH_TYPE
          valueFrom:
            secretKeyRef:
              name: storage-secret
              key: AZURE_STORAGE_AUTH_TYPE
           - name: AZURE_STORAGE_ACCOUNT
          valueFrom:
            secretKeyRef:
              name: storage-secret
              key: AZURE_STORAGE_ACCOUNT
        - name: AZURE_STORAGE_ACCOUNT_TYPE
          valueFrom:
            secretKeyRef:
              name: storage-secret
              key: AZURE_STORAGE_ACCOUNT_TYPE  

```


You can verify by exec-ing a shell inside the pod

```
$ kubectl exec -it bp-test -- bash                                                                                            13s ⎈ admin
root@test:/# cd /blobdata/
root@test:/blobdata# ls -ltr
total 0
-rwxr-xr-x 1 root root    0 Nov 23 13:25 t
-rwxr-xr-x 1 root root 8220 Nov 23 13:27 test-state.json
-rwxr-xr-x 1 root root    6 Nov 23 16:36 text
-rwxr-xr-x 1 root root    0 Nov 23 16:45 test-2
```
