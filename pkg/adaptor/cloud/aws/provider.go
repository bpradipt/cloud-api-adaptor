// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/netip"
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
	DescribeInstanceTypes(ctx context.Context,
		params *ec2.DescribeInstanceTypesInput,
		optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error)
	CreateTags(ctx context.Context,
		params *ec2.CreateTagsInput,
		optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	ModifyInstanceAttribute(ctx context.Context,
		params *ec2.ModifyInstanceAttributeInput,
		optFns ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error)
	StopInstances(ctx context.Context,
		params *ec2.StopInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
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

	if err = provider.updateInstanceTypeSpecList(); err != nil {
		return nil, err
	}

	// Initialise VM pool
	// Precreate instances
	if config.PoolSize > 0 {
		if err := provider.initializePodVmPool(context.TODO(), config.PoolSize); err != nil {
			return nil, err
		}
	}

	return provider, nil
}

func getIPs(instance types.Instance) ([]netip.Addr, error) {

	var podNodeIPs []netip.Addr
	for i, nic := range instance.NetworkInterfaces {
		addr := nic.PrivateIpAddress

		if addr == nil || *addr == "" || *addr == "0.0.0.0" {
			return nil, errNotReady
		}

		ip, err := netip.ParseAddr(*addr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse pod node IP %q: %w", *addr, err)
		}
		podNodeIPs = append(podNodeIPs, ip)

		logger.Printf("podNodeIP[%d]=%s", i, ip.String())
	}

	return podNodeIPs, nil
}

func (p *awsProvider) CreateInstance(ctx context.Context, podName, sandboxID string, cloudConfig cloudinit.CloudConfigGenerator, spec cloud.InstanceTypeSpec) (*cloud.Instance, error) {

	// cloud.Instance var
	var instance cloud.Instance

	instanceName := util.GenerateInstanceName(podName, sandboxID, maxInstanceNameLen)

	userData, err := cloudConfig.Generate()
	if err != nil {
		return nil, err
	}

	//Convert userData to base64
	userDataEnc := base64.StdEncoding.EncodeToString([]byte(userData))

	instanceType, err := p.selectInstanceType(ctx, spec)
	if err != nil {
		return nil, err
	}

	instanceTags := []types.Tag{
		{
			Key:   aws.String("Name"),
			Value: aws.String(instanceName),
		},
	}

	// Add custom tags (k=v) from serviceConfig.Tags to the instance
	for k, v := range p.serviceConfig.Tags {
		instanceTags = append(instanceTags, types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	// Create TagSpecifications for the instance
	tagSpecifications := []types.TagSpecification{
		{
			ResourceType: types.ResourceTypeInstance,
			Tags:         instanceTags,
		},
	}

	var input *ec2.RunInstancesInput

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

		// Create tagsInput based on instanceTags for the instance
		tagsInput := &ec2.CreateTagsInput{
			Resources: []string{instance.ID},
			Tags:      instanceTags,
		}

		_, err = p.ec2Client.CreateTags(ctx, tagsInput)
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
		// Log the instance struct
		logger.Printf("Instance details from the pool: %#v", instance)
	} else {

		if p.serviceConfig.UseLaunchTemplate {
			input = &ec2.RunInstancesInput{
				MinCount: aws.Int32(1),
				MaxCount: aws.Int32(1),
				LaunchTemplate: &types.LaunchTemplateSpecification{
					LaunchTemplateName: aws.String(p.serviceConfig.LaunchTemplateName),
				},
				UserData:          &userDataEnc,
				TagSpecifications: tagSpecifications,
			}
		} else {
			input = &ec2.RunInstancesInput{
				MinCount:          aws.Int32(1),
				MaxCount:          aws.Int32(1),
				ImageId:           aws.String(p.serviceConfig.ImageId),
				InstanceType:      types.InstanceType(instanceType),
				SecurityGroupIds:  p.serviceConfig.SecurityGroupIds,
				SubnetId:          aws.String(p.serviceConfig.SubnetId),
				UserData:          &userDataEnc,
				TagSpecifications: tagSpecifications,
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

		instanceID := *result.Instances[0].InstanceId

		ips, err := getIPs(result.Instances[0])
		if err != nil {
			logger.Printf("failed to get IPs for the instance : %v ", err)
			return nil, err
		}

		instance.ID = instanceID
		instance.Name = instanceName
		instance.IPs = ips

	}
	return &instance, nil
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

// Add SelectInstanceType method to select an instance type based on the memory and vcpu requirements
func (p *awsProvider) selectInstanceType(ctx context.Context, spec cloud.InstanceTypeSpec) (string, error) {

	return cloud.SelectInstanceTypeToUse(spec, p.serviceConfig.InstanceTypeSpecList, p.serviceConfig.InstanceTypes, p.serviceConfig.InstanceType)
}

// Add a method to populate InstanceTypeSpecList for all the instanceTypes
func (p *awsProvider) updateInstanceTypeSpecList() error {

	// Get the instance types from the service config
	instanceTypes := p.serviceConfig.InstanceTypes

	// If instanceTypes is empty then populate it with the default instance type
	if len(instanceTypes) == 0 {
		instanceTypes = append(instanceTypes, p.serviceConfig.InstanceType)
	}

	// Create a list of instancetypespec
	var instanceTypeSpecList []cloud.InstanceTypeSpec

	// Iterate over the instance types and populate the instanceTypeSpecList
	for _, instanceType := range instanceTypes {
		vcpus, memory, err := p.getInstanceTypeInformation(instanceType)
		if err != nil {
			return err
		}
		instanceTypeSpecList = append(instanceTypeSpecList, cloud.InstanceTypeSpec{InstanceType: instanceType, VCPUs: vcpus, Memory: memory})
	}

	// Sort the instanceTypeSpecList by Memory and update the serviceConfig
	p.serviceConfig.InstanceTypeSpecList = cloud.SortInstanceTypesOnMemory(instanceTypeSpecList)
	logger.Printf("InstanceTypeSpecList (%v)", p.serviceConfig.InstanceTypeSpecList)
	return nil
}

// Add a method to retrieve cpu, memory, and storage from the instance type
func (p *awsProvider) getInstanceTypeInformation(instanceType string) (vcpu int64, memory int64, err error) {

	// Get the instance type information from the instance type using AWS API
	input := &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{
			types.InstanceType(instanceType),
		},
	}
	// Get the instance type information from the instance type using AWS API
	result, err := p.ec2Client.DescribeInstanceTypes(context.Background(), input)
	if err != nil {
		return 0, 0, err
	}

	// Get the vcpu and memory from the result
	if len(result.InstanceTypes) > 0 {
		vcpu = int64(*result.InstanceTypes[0].VCpuInfo.DefaultVCpus)
		memory = int64(*result.InstanceTypes[0].MemoryInfo.SizeInMiB)
		return vcpu, memory, nil
	}
	return 0, 0, fmt.Errorf("instance type %s not found", instanceType)

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
	// TBD: This might not be required as we are switching the instances to stopped state
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
