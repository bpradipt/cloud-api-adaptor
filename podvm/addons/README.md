## Introduction

The addons directory is used to enable different addons for the podvm image.
Each addon and its associated files (binaries, configuration etc) should be under
specific sub-dir under `addons`. 

Each addon needs to have `setup.sh` for setting up the addon.
To enable the addon create a file named `.enable` in the top level podvm/addons
directory and in the specific addon sub-dir.

The addons are setup by the `qcow2/setup_addons.sh` script.

Two addons are provided as examples:
- `gpu`: This addon enables nvidia GPU support in the podvm image
- `blobfuse`: This addon enables Azure blobfuse to mount blobs inside pod VM for use by the pods

By default all addons are disabled. If you want to enable `gpu` addon, then you will need to do the following:

```
touch podvm/addons/.enable
touch podvm/addons/gpu/.enable
```
