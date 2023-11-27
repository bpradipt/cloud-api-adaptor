#!/bin/bash


#This is the dir in the pod vm image during build
ADDONS_DIR="/tmp/addons"


# Copy policy file
mkdir -p /etc/kata-opa

cp ${ADDONS_DIR}/opa/allow-all.rego /etc/kata-opa
cp ${ADDONS_DIR}/opa/allow-all-except-exec-process.rego /etc/kata-opa

# Create default rego policy
ln -s /etc/kata-opa/allow-all-except-exec-process.rego /etc/kata-opa/default-policy.rego


# Create service file

cp ${ADDONS_DIR}/opa/kata-opa.service /etc/systemd/system/kata-opa.service

systemctl enable kata-opa.service

# PODVM_DISTRO variable is set as part of the podvm image build process
# and available inside the packer VM
if  [[ "$PODVM_DISTRO" == "ubuntu" ]] || [[ "$PODVM_DISTRO" == "rhel" ]]; then
	# Copy opa binary in /usr/local/bin
	curl -L -o opa https://openpolicyagent.org/downloads/v0.58.0/opa_linux_amd64_static
	install -D -o root -g root -m 0755 opa -T /usr/local/bin/opa

fi



