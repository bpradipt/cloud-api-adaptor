#!/bin/bash

#This is the dir in the pod vm image during build
ADDONS_DIR="/tmp/addons"

# Create the prestart hook directory
mkdir -p /usr/share/oci/hooks/prestart

# Copy hook binary
cp ${ADDONS_DIR}/mountpoint/mountpoint-oci-hook  /usr/share/oci/hooks/prestart


# Copy hook config
cp ${ADDONS_DIR}/mountpoint/mountpoint_hookconfig.json  /usr/share/oci/hooks


# PODVM_DISTRO variable is set as part of the podvm image build process
# and available inside the packer VM
# Add NVIDIA packages
if  [[ "$PODVM_DISTRO" == "ubuntu" ]]; then
    export DEBIAN_FRONTEND=noninteractive
    wget https://s3.amazonaws.com/mountpoint-s3-release/latest/x86_64/mount-s3.deb
    apt-get install -q -y ./mount-s3.deb
    rm -f mount-s3.deb
fi
if  [[ "$PODVM_DISTRO" == "rhel" ]]; then
    wget https://s3.amazonaws.com/mountpoint-s3-release/latest/x86_64/mount-s3.rpm
    yum install -q -y ./mount-s3.rpm
    rm -f mount-s3.rpm
fi


