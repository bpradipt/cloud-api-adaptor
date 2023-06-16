#!/bin/bash

apt-get update -y && apt-get install -y gcc make binutils

wget https://us.download.nvidia.com/XFree86/Linux-x86_64/470.182.03/NVIDIA-Linux-x86_64-470.182.03.run
chmod +x NVIDIA-Linux-x86_64-470.182.03.run
./NVIDIA-Linux-x86_64-470.182.03.run



distribution=$(. /etc/os-release;echo $ID$VERSION_ID)
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -s -L https://nvidia.github.io/libnvidia-container/$distribution/libnvidia-container.list | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
apt update
apt install nvidia-container-toolkit



mkdir -p /usr/share/oci/hooks/prestart


cat <<'END' > /usr/share/oci/hooks/prestart/nvidia-container-toolkit.sh
#!/bin/bash -x
/usr/bin/nvidia-container-toolkit -debug $@
END

chmod +x /usr/share/oci/hooks/prestart/nvidia-container-toolkit.sh
