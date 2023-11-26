#!/bin/bash

#This is the dir in the pod vm image during build
ADDONS_DIR="/tmp/addons"

# Create the prestart hook directory
mkdir -p /usr/share/oci/hooks/prestart

# Copy hook binary
cp ${ADDONS_DIR}/blobfuse/blobfuse-oci-hook  /usr/share/oci/hooks/prestart

# Copy blobfuse config
cp ${ADDONS_DIR}/blobfuse/blobfuseconfig.yaml  /etc

# Copy hook config
cp ${ADDONS_DIR}/blobfuse/blobfuse_hookconfig.json  /usr/share/oci/hooks


# PODVM_DISTRO variable is set as part of the podvm image build process
# and available inside the packer VM
# Add NVIDIA packages
if  [[ "$PODVM_DISTRO" == "ubuntu" ]]; then
    export DEBIAN_FRONTEND=noninteractive
    wget https://packages.microsoft.com/config/ubuntu/22.04/packages-microsoft-prod.deb
    dpkg -i packages-microsoft-prod.deb
    apt-get -q update
    apt-get -q install -y libfuse3-dev fuse3 blobfuse2

fi
if  [[ "$PODVM_DISTRO" == "rhel" ]]; then
    rpm -Uvh https://packages.microsoft.com/config/rhel/9/packages-microsoft-prod.rpm
    yum install -y -q blobfuse2
fi
