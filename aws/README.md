# Setup instructions

- Install AWS CLI
Follow the instructions [here](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) to install the AWS CLI for your platform

- Export AWS variables
```
set +o history
export AWS_ACCESS_KEY_ID="REPLACE_ME"
export AWS_SECRET_ACCESS_KEY="REPLACE_ME"
export REGION="REPLACE_ME"
export ACCOUNT_ID="REPLACE_ME"
set -o history
```
- Create a VPC with public internet access
```
cd image
. ./create-vpc
```
- Create a custom AMI based on Ubuntu 20.04 having kata-agent and other dependencies.
```
make build
```
export the AMI ID
```
export AMI_ID=<ami-id-returned-above>
```
- Create an EC2 launch template named "kata". 
```
. ./create-lt
```


# Running cloud-api-adaptor

```
cloud-api-adaptor-aws aws \
    -aws-access-key-id ${AWS_ACCESS_KEY_ID} \
    -aws-secret-key ${AWS_SECRET_ACCESS_KEY} \
    -aws-region ${AWS_REGION} \
    -pods-dir /run/peerpod/pods \
    -socket /run/peerpod/hypervisor.sock
```

