// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// TODO: Use IAM role
func NewEC2Client(cloudCfg Config) (*ec2.Client, error) {

	var cfg aws.Config
	var err error

	if cloudCfg.AccessKeyId != "" && cloudCfg.SecretKey != "" {
		cfg, err = config.LoadDefaultConfig(context.TODO(),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cloudCfg.AccessKeyId, cloudCfg.SecretKey, "")), config.WithRegion(cloudCfg.Region))
		if err != nil {
			return nil, fmt.Errorf("configuration error when using creds: %s", err)
		}

	} else {

		cfg, err = config.LoadDefaultConfig(context.TODO(),
			config.WithRegion(cloudCfg.Region),
			config.WithSharedConfigProfile(cloudCfg.LoginProfile))
		if err != nil {
			return nil, fmt.Errorf("configuration error when using shared profile: %s", err)
		}
	}
	client := ec2.NewFromConfig(cfg)

	return client, nil
}

// Add method to create ec2.Client using temporary credentials from STS
func NewEC2ClientWithSTS(cloudCfg Config) (*ec2.Client, error) {
	// Create new AWS SDK v2 Config
	var cfg aws.Config
	var err error

	// Load default config with region
	cfg, err = config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(cloudCfg.Region))
	if err != nil {
		return nil, fmt.Errorf("configuration error when using shared profile: %s", err)
	}
	// Create an STS client
	stsClient := sts.NewFromConfig(cfg)

	// Get the caller identity to determine the current IAM role or user
	identityResp, err := stsClient.GetCallerIdentity(context.TODO(), &sts.GetCallerIdentityInput{})
	if err != nil {
		fmt.Println("Failed to get caller identity:", err)
		return nil, err
	}

	// ARN for EC2 RunInstances API
	// arn:aws:iam::aws:policy/AmazonEC2FullAccess

	// Create credential provider for the assumed role using the current IAM role *identityResp.Account
	credsProvider := stscreds.NewAssumeRoleProvider(stsClient, "arn:aws:iam::aws:policy/AmazonEC2FullAccess", func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "cloud-api-adaptor"
		o.SourceIdentity = identityResp.Account
	})

	// Create the EC2 client using the temporary credentials
	ec2Client := ec2.NewFromConfig(cfg, func(options *ec2.Options) {
		options.Credentials = credsProvider
	})

	return ec2Client, nil

}
