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

	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/cloud"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/cloudinit"
)

var logger = log.New(log.Writer(), "[adaptor/cloud/aws] ", log.LstdFlags|log.Lmsgprefix)
var errNotReady = errors.New("address not ready")

const (
	maxInstanceNameLen = 63
	// Add maxWaitTime to allow for instance to be ready
	maxWaitTime = 120 * time.Second
)

// Make ec2Client a mockable interface
type ec2Client interface {
	RunInstances(ctx context.Context,
		params *ec2.RunInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeInstances(ctx context.Context,
		params *ec2.DescribeInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(ctx context.Context,
		params *ec2.TerminateInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	CreateTags(ctx context.Context,
		params *ec2.CreateTagsInput,
		optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	// Add ModifyInstanceAttribute method
	ModifyInstanceAttribute(ctx context.Context,
		params *ec2.ModifyInstanceAttributeInput,
		optFns ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error)
	// Add StopeInstances method
	StopInstances(ctx context.Context,
		params *ec2.StopInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	// Add StartInstances method
	StartInstances(ctx context.Context,
		params *ec2.StartInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
}

type awsProvider struct {
	// Make ec2Client a mockable interface
	ec2Client     ec2Client
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

	// Precreate instances
	if config.DesiredPoolSize > 0 {
		if err := provider.initializePodVmPool(context.TODO(), config.DesiredPoolSize); err != nil {
			return nil, err
		}
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

	// cloud.Instance var
	var instance cloud.Instance

	instanceName := util.GenerateInstanceName(podName, sandboxID, maxInstanceNameLen)

	userData, err := cloudConfig.Generate()
	if err != nil {
		return nil, err
	}

	//Convert userData to base64
	userDataEnc := base64.StdEncoding.EncodeToString([]byte(userData))

	// Check if pre-created instances are available
	// If so, use one of them
	if len(p.serviceConfig.PreCreatedInstances) > 0 {
		// Get the first pre-created instance
		instance = p.serviceConfig.PreCreatedInstances[0]
		// Remove the first pre-created instance from the list
		p.serviceConfig.PreCreatedInstances = p.serviceConfig.PreCreatedInstances[1:]

		// Update the instance name of pre-created instance with the generated instance name
		instance.Name = instanceName

		logger.Printf("Using instance(%s) from precreated pool for %s", instance.ID, instanceName)

		// Modify the instance attribute to set the instance id and shutdown behaviour
		_, err := p.ec2Client.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
			InstanceId: aws.String(instance.ID),
			// Update the instance shutdown behaviour to terminate
			InstanceInitiatedShutdownBehavior: &types.AttributeValue{
				Value: aws.String("terminate"),
			},
		})
		if err != nil {
			return nil, err
		}

		// Different attribute types need to be handled separately

		// Modify the instance attribute to set userData
		_, err = p.ec2Client.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
			InstanceId: aws.String(instance.ID),
			// Update the instance userData
			UserData: &types.BlobAttributeValue{
				Value: []byte(userDataEnc),
			},
		})
		if err != nil {
			return nil, err
		}

		// Create tagInput to add instance name tag and custom tags to the instance
		tagInput := &ec2.CreateTagsInput{
			Resources: []string{instance.ID},
			Tags: []types.Tag{
				{
					Key:   aws.String("Name"),
					Value: aws.String(instanceName),
				},
			},
		}

		// Add custom tags (k=v) from serviceConfig.Tags to the instance
		for k, v := range p.serviceConfig.Tags {
			tagInput.Tags = append(tagInput.Tags, types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}

		_, err = p.ec2Client.CreateTags(ctx, tagInput)
		if err != nil {
			logger.Printf("Adding tags to the instance failed with error: %s", err)
		}

		// Start the instance
		_, err = p.ec2Client.StartInstances(ctx, &ec2.StartInstancesInput{
			InstanceIds: []string{instance.ID},
		})
		if err != nil {
			return nil, err
		}

		// Check if UsePublicIP is set to true
		if p.serviceConfig.UsePublicIP {
			// Add describe instance input
			describeInstanceInput := &ec2.DescribeInstancesInput{
				InstanceIds: []string{instance.ID},
			}

			// Create New InstanceRunningWaiter
			waiter := ec2.NewInstanceRunningWaiter(p.ec2Client)

			// Wait for instance to be ready before getting the public IP address
			err := waiter.Wait(ctx, describeInstanceInput, maxWaitTime)
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

			// Replace the IPs []net.IP array first element in the instance struct with the public IP address
			instance.IPs[0] = publicIPAddr

		}

		// Log the instance struct
		logger.Printf("Instance details from the pool: %#v", instance)

		return &instance, nil

	} else {
		// Precreated instances are not available. So create a new instance

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

		// Add custom tags (k=v) from serviceConfig.Tags to the instance
		for k, v := range p.serviceConfig.Tags {
			tagInput.Tags = append(tagInput.Tags, types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
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

			// Wait for instance to be ready before getting the public IP address
			err := waiter.Wait(ctx, describeInstanceInput, maxWaitTime)
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

func (p *awsProvider) modifyInstanceUserData(ctx context.Context, instanceID string, userDataEnc []byte) error {
	// Add modify instance attribute input
	modifyInstanceAttributeInput := &ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		UserData: &types.BlobAttributeValue{
			Value: userDataEnc,
		},
	}

	// Modify the instance attribute
	_, err := p.ec2Client.ModifyInstanceAttribute(ctx, modifyInstanceAttributeInput)
	if err != nil {
		logger.Printf("failed to modify the instance attribute : %v ", err)
		return err
	}

	return nil
}

// Add a method to precreate some instances in stopped state using ec2Client.RunInstances
// Take the number of instances to be created as an argument
// Take the RunInstancesInput parameters from serviceConfig
// Return the cloud.Instance slice
func (p *awsProvider) initializePodVmPool(ctx context.Context, numInstances int) error {

	// Create a slice of cloud.Instance
	instances := make([]cloud.Instance, numInstances)

	// Create a slice of RunInstancesInput
	runInstancesInput := make([]*ec2.RunInstancesInput, numInstances)

	// Create a slice of RunInstancesOutput
	runInstancesOutput := make([]*ec2.RunInstancesOutput, numInstances)

	// Create RunInstancesInput for each instance
	// Precreated instances are of one type and one image
	// Precreated instances cannot be customized using pod annotations
	for i := 0; i < numInstances; i++ {
		runInstancesInput[i] = &ec2.RunInstancesInput{
			ImageId:          aws.String(p.serviceConfig.ImageId),
			InstanceType:     types.InstanceType(p.serviceConfig.InstanceType),
			MaxCount:         aws.Int32(1),
			MinCount:         aws.Int32(1),
			SecurityGroupIds: p.serviceConfig.SecurityGroupIds,
			SubnetId:         aws.String(p.serviceConfig.SubnetId),
			// Don't delete the instance on shutdown. We'll change this to terminate later.
			InstanceInitiatedShutdownBehavior: types.ShutdownBehaviorStop,
			//UserData:         &userDataEnc,
		}
		if p.serviceConfig.KeyName != "" {
			runInstancesInput[i].KeyName = aws.String(p.serviceConfig.KeyName)
		}
	}

	// Create instances
	var err error
	for i := 0; i < numInstances; i++ {
		runInstancesOutput[i], err = p.ec2Client.RunInstances(ctx, runInstancesInput[i])
		if err != nil {
			logger.Printf("failed to create instances : %v ", err)
			return err
		}
	}

	// Get the ip addresses for each instance using getIPs
	for i := 0; i < numInstances; i++ {
		ips, err := getIPs(runInstancesOutput[i].Instances[0])
		if err != nil {
			logger.Printf("failed to get IPs for the instance : %v ", err)
			return err
		}

		instance := cloud.Instance{
			ID:   *runInstancesOutput[i].Instances[0].InstanceId,
			Name: *runInstancesOutput[i].Instances[0].InstanceId,
			IPs:  ips,
		}
		instances[i] = instance
	}

	// Wait for the instances to be in Running state
	for i := 0; i < numInstances; i++ {
		describeInstanceInput := &ec2.DescribeInstancesInput{
			InstanceIds: []string{*runInstancesOutput[i].Instances[0].InstanceId},
		}

		// Create New InstanceRunningWaiter
		waiter := ec2.NewInstanceRunningWaiter(p.ec2Client)

		// Wait for instance to be in Running state
		err := waiter.Wait(ctx, describeInstanceInput, maxWaitTime)
		if err != nil {
			logger.Printf("failed to wait for the instance to be ready : %v ", err)
			return err
		}
	}

	// Stop the instances
	for i := 0; i < numInstances; i++ {
		stopInstanceInput := &ec2.StopInstancesInput{
			InstanceIds: []string{*runInstancesOutput[i].Instances[0].InstanceId},
		}

		_, err := p.ec2Client.StopInstances(ctx, stopInstanceInput)
		if err != nil {
			logger.Printf("failed to stop the instance : %v ", err)
			return err
		}
	}

	// Update config.PreCreatedInstances with the instances
	p.serviceConfig.PreCreatedInstances = instances

	return nil
}
