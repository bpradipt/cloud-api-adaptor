## Introduction

This addon enables AWS mountpoint to mount S3 bucket inside pod VM for use by the pod.

Sample configurations are provided here. You'll also need the oci hook.
A sample oci hook for blobfuse can be downloaded by following the instructions below:

```
wget https://github.com/bpradipt/mountpoint-oci-hook/releases/download/v0.0.1/mountpoint-oci-hook-v0.0.1-linux-amd64.tar.gz
tar zxvf mountpoint-oci-hook-v0.0.1-linux-amd64.tar.gz
```
The binary `mountpoint-oci-hook` should be placed under `podvm/addons/mountpoint` directory.

The hook expects a configuration file which is a json. An example configuration file is shown below and is 
available under `podvm/addons/mountpoint`
```
{
  "activation_flag": "HOOK",
  "program_path": "/usr/bin/mount-s3",
  "host_mountpoint": "/s3data",
  "container_mountpoint": "/s3data"
}
```

The value of the `activation_flag` (ie `HOOK` as shown above) needs to be provided as environment variable to the container.
The `container_mountpoint` can also be provided as an environment variable (`CONTAINER_MOUNTPOINT`) in the container using this hook.

Further, the mountpoint auth and other related parameters need to be provided as environment variables. 
Details of the mountpoint environment variables can be found [here](https://github.com/awslabs/mountpoint-s3#readme).

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
        - name: S3_BUCKET
          value: "my_bucket"
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: s3-secret
              key: AWS_ACCESS_KEY_ID
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: s3-secret
              key: AWS_SECRET_ACCESS_KEY

```


You can verify by exec-ing a shell inside the pod

```
$ kubectl exec -it bp-test -- bash                                                                                            13s ⎈ admin
root@test:/# cd /s3data/
root@test:/s3data# ls -ltr
total 0
-rwxr-xr-x 1 root root    0 Nov 23 13:25 t
-rwxr-xr-x 1 root root 8220 Nov 23 13:27 test-state.json
-rwxr-xr-x 1 root root    6 Nov 23 16:36 text
-rwxr-xr-x 1 root root    0 Nov 23 16:45 test-2
```
