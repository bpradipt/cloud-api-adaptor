// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go/aws/request"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/cloud"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/cloudinit"
)

var logger = log.New(log.Writer(), "[adaptor/cloud/aws] ", log.LstdFlags|log.Lmsgprefix)
var errNotReady = errors.New("address not ready")

const maxInstanceNameLen = 63

type awsProvider struct {
	ec2Client     *ec2.Client
	serviceConfig *Config
}

func NewProvider(config *Config) (cloud.Provider, error) {

	logger.Printf("aws config: %#v", config.Redact())

	if err := retrieveMissingConfig(config); err != nil {
		logger.Printf("Failed to retrieve configuration, some fields may still be missing: %v", err)
	}

	ec2Client, err := NewEC2Client(*config)
	if err != nil {
		return nil, err
	}

	provider := &awsProvider{
		ec2Client:     ec2Client,
		serviceConfig: config,
	}

	return provider, nil
}

func getIPs(instance types.Instance) ([]net.IP, error) {

	var podNodeIPs []net.IP
	for i, nic := range instance.NetworkInterfaces {
		addr := nic.PrivateIpAddress

		if addr == nil || *addr == "" || *addr == "0.0.0.0" {
			return nil, errNotReady
		}

		ip := net.ParseIP(*addr)
		if ip == nil {
			return nil, fmt.Errorf("failed to parse pod node IP %q", *addr)
		}
		podNodeIPs = append(podNodeIPs, ip)

		logger.Printf("podNodeIP[%d]=%s", i, ip.String())
	}

	return podNodeIPs, nil
}

func (p *awsProvider) CreateInstance(ctx context.Context, podName, sandboxID string, cloudConfig cloudinit.CloudConfigGenerator, instanceType string) (*cloud.Instance, error) {

	instanceName := util.GenerateInstanceName(podName, sandboxID, maxInstanceNameLen)

	userData, err := cloudConfig.Generate()
	if err != nil {
		return nil, err
	}

	//Convert userData to base64
	userDataEnc := base64.StdEncoding.EncodeToString([]byte(userData))

	// Check if instanceType is empty
	// If it's not empty, it means it's set via the pod annotation
	if instanceType == "" {
		// Set instanceType to default instance type if instanceType is empty
		instanceType = p.serviceConfig.InstanceType
	}

	// If instanceTypes is empty and instanceType is not default instance type, return error
	if len(p.serviceConfig.InstanceTypes) == 0 && instanceType != p.serviceConfig.InstanceType {
		return nil, fmt.Errorf("instance type %q is not supported.", instanceType)
	}

	// Check if instanceType is among the supported instance types only if instanceTypes is not empty.
	if len(p.serviceConfig.InstanceTypes) > 0 {
		if !util.Contains(p.serviceConfig.InstanceTypes, instanceType) {
			return nil, fmt.Errorf("instance type %q is not supported", instanceType)
		}
	}

	var input *ec2.RunInstancesInput

	if p.serviceConfig.UseLaunchTemplate {
		input = &ec2.RunInstancesInput{
			MinCount: aws.Int32(1),
			MaxCount: aws.Int32(1),
			LaunchTemplate: &types.LaunchTemplateSpecification{
				LaunchTemplateName: aws.String(p.serviceConfig.LaunchTemplateName),
			},
			UserData: &userDataEnc,
		}
	} else {
		input = &ec2.RunInstancesInput{
			MinCount:         aws.Int32(1),
			MaxCount:         aws.Int32(1),
			ImageId:          aws.String(p.serviceConfig.ImageId),
			InstanceType:     types.InstanceType(p.serviceConfig.InstanceType),
			SecurityGroupIds: p.serviceConfig.SecurityGroupIds,
			SubnetId:         aws.String(p.serviceConfig.SubnetId),
			UserData:         &userDataEnc,
		}
		if p.serviceConfig.KeyName != "" {
			input.KeyName = aws.String(p.serviceConfig.KeyName)
		}
	}

	logger.Printf("CreateInstance: name: %q", instanceName)

	result, err := p.ec2Client.RunInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("Creating instance (%v) returned error: %s", result, err)
	}

	logger.Printf("created an instance %s for sandbox %s", *result.Instances[0].PublicDnsName, sandboxID)

	tagInput := &ec2.CreateTagsInput{
		Resources: []string{*result.Instances[0].InstanceId},
		Tags: []types.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(instanceName),
			},
		},
	}

	_, err = p.ec2Client.CreateTags(ctx, tagInput)
	if err != nil {
		logger.Printf("Adding tags to the instance failed with error: %s", err)
	}

	instanceID := *result.Instances[0].InstanceId

	ips, err := getIPs(result.Instances[0])
	if err != nil {
		logger.Printf("failed to get IPs for the instance : %v ", err)
		return nil, err
	}

	// Check if UsePublicIP is set to true
	if p.serviceConfig.UsePublicIP {
		// Add describe instance input
		describeInstanceInput := &ec2.DescribeInstancesInput{
			InstanceIds: []string{instanceID},
		}

		// Create New InstanceRunningWaiter
		waiter := ec2.NewInstanceRunningWaiter(p.ec2Client)

		// Wait for the instance to be ready using InstanceRunningWaiter
		err = waiter.Wait(ctx, describeInstanceInput, func(w *request.Waiter) {
			w.MaxAttempts = 100
			w.Delay = request.ConstantWaiterDelay(5 * time.Second)
		})
		if err != nil {
			logger.Printf("failed to wait for the instance to be ready : %v ", err)
			return nil, err
		}
		// Add describe instance output
		describeInstanceOutput, err := p.ec2Client.DescribeInstances(ctx, describeInstanceInput)
		if err != nil {
			logger.Printf("failed to describe the instance : %v ", err)
			return nil, err
		}
		// Get the public IP address
		publicIP := describeInstanceOutput.Reservations[0].Instances[0].PublicIpAddress
		// Check if the public IP address is nil
		if publicIP == nil {
			return nil, fmt.Errorf("public IP address is nil")
		}
		// If the public IP address is empty, return an error
		if *publicIP == "" {
			return nil, fmt.Errorf("public IP address is empty")
		}

		// Parse the public IP address
		publicIPAddr := net.ParseIP(*publicIP)
		if publicIPAddr == nil {
			return nil, fmt.Errorf("failed to parse public IP address %q", *publicIP)
		}

		// Replace the private IP address with the public IP address
		ips[0] = publicIPAddr

	}

	instance := &cloud.Instance{
		ID:   instanceID,
		Name: instanceName,
		IPs:  ips,
	}

	return instance, nil
}

func (p *awsProvider) DeleteInstance(ctx context.Context, instanceID string) error {
	terminateInput := &ec2.TerminateInstancesInput{
		InstanceIds: []string{
			instanceID,
		},
	}

	resp, err := p.ec2Client.TerminateInstances(ctx, terminateInput)

	if err != nil {
		logger.Printf("failed to delete an instance: %v and the response is %v", err, resp)
		return err
	}
	logger.Printf("deleted an instance %s", instanceID)
	return nil

}

func (p *awsProvider) Teardown() error {
	return nil
}
