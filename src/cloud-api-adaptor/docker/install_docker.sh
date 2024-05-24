#!/bin/bash

# Accept two arguments: install and uninstall

# Install Docker
if [ "$1" == "install" ]; then
    # Check if Docker is already installed
    if [ -x "$(command -v docker)" ]; then
        echo "Docker is already installed"
    else
        # Install Docker
        echo "Installing Docker"
        curl -fsSL https://get.docker.com -o get-docker.sh || exit 1
        sudo sh get-docker.sh || exit 1
        sudo groupadd docker
        sudo usermod -aG docker $USER
    fi
    exit 0
fi
# Uninstall Docker
if [ "$1" == "uninstall" ]; then
    # Check if Docker is installed
    if [ ! -x "$(command -v docker)" ]; then
        echo "Docker is not installed"
        exit 0
    fi

    # Uninstall Docker
    echo "Uninstalling Docker"
    # Check if OS is Ubuntu
    if [ -x "$(command -v apt-get)" ]; then
        sudo apt-get purge -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-ce-rootless-extras
        sudo rm -rf /var/lib/docker
        sudo rm -rf /var/lib/containerd
        exit 0
    elif [ -x "$(command -v dnf)" ]; then
        sudo dnf remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-ce-rootless-extras
        sudo rm -rf /var/lib/docker
        sudo rm -rf /var/lib/containerd
        exit 0
    fi

    exit 0
fi
