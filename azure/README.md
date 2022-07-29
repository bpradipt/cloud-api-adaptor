# Setup instructions

- Install packer by following the instructions in the following [link](https://learn.hashicorp.com/tutorials/packer/get-started-install-cli)

- Create a Resource Group and Service Principal

- Set environment variables
```
export CLIENT_ID="REPLACE_ME"
export CLIENT_SECRET="REPLACE_ME"
export SUBSCRIPTION_ID="REPLACE_ME"
export TENANT_ID="REPLACE_ME"
export LOCATION="REPLACE_ME"
export VM_SIZE="REPLACE_ME"
export RESOURCE_GROUP_NAME="REPLACE_ME"
```

- Create a custom Azure VM image based on Ubuntu 20.04 having kata-agent and other dependencies.
```
CLOUD_PROVIDER=azure make build
```


# Running cloud-api-adaptor


