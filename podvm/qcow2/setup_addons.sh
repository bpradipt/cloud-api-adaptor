#!/bin/bash

# This is the dir in the pod vm image during build
ADDONS_DIR="/tmp/addons"

# Check if the global .enable file exists
if [ -e "${ADDONS_DIR}/.enable" ]; then
    echo "Global Enable: Configuring add-ons."

    # Iterate over subdirectories under ADDONS_DIR
    for addon_dir in "${ADDONS_DIR}"/*/; do
        addon_name=$(basename "${addon_dir}")

        # Check if .enable file exists in the addon subdirectory
        if [ -e "${addon_dir}/.enable" ]; then
            echo "Configuring ${addon_name}..."
            "${addon_dir}"/setup.sh
        else
            echo "${addon_name} is disabled."
        fi
    done

else
    echo "Global Disable: Noop (No add-ons configured)."
fi

