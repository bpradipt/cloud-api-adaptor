// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/cloud"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/cloudinit"
)

// Mock EC2 API
type mockEC2Client struct{}

// Return a new mock EC2 API
func newMockEC2Client() *mockEC2Client {
	return &mockEC2Client{}
}

// Create a mock EC2 RunInstances method
func (m mockEC2Client) RunInstances(ctx context.Context,
	params *ec2.RunInstancesInput,
	optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {

	// Create a mock instance ID
	mockInstanceID := "i-1234567890abcdef0"
	// Return a mock RunInstancesOutput
	return &ec2.RunInstancesOutput{
		Instances: []types.Instance{
			{
				InstanceId: &mockInstanceID,
				// Add public DNS name
				PublicDnsName: aws.String("ec2-192-168-100-1.compute-1.amazonaws.com"),
				// Add private IP address to mock instance
				PrivateIpAddress: aws.String("10.0.0.2"),
				// Add private IP address to network interface
				NetworkInterfaces: []types.InstanceNetworkInterface{
					{
						PrivateIpAddress: aws.String("10.0.0.2"),
					},
				},
			},
		},
	}, nil
}

// Create a mock EC2 DescribeInstances method
func (m mockEC2Client) DescribeInstances(ctx context.Context,
	params *ec2.DescribeInstancesInput,
	optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {

	// Create a mock instance ID
	mockInstanceID := "i-1234567890abcdef0"
	// Return a mock DescribeInstancesOutput
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{
			{
				Instances: []types.Instance{
					{
						InstanceId: &mockInstanceID,
						// Add public DNS name
						PublicDnsName: aws.String("ec2-192-168-100-1.compute-1.amazonaws.com"),
						// Add private IP address to mock instance
						PrivateIpAddress: aws.String("10.0.0.2"),
						// Add private IP address to network interface
						NetworkInterfaces: []types.InstanceNetworkInterface{
							{
								PrivateIpAddress: aws.String("10.0.0.2"),
							},
						},
					},
				},
			},
		},
	}, nil
}

// Create a mock CreateTags method
func (m mockEC2Client) CreateTags(ctx context.Context,
	params *ec2.CreateTagsInput,
	optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {

	// Return a mock CreateTagsOutput
	return &ec2.CreateTagsOutput{}, nil
}

// Create a mock EC2 TerminateInstances method
func (m mockEC2Client) TerminateInstances(ctx context.Context,
	params *ec2.TerminateInstancesInput,
	optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {

	// Return a mock TerminateInstancesOutput
	return &ec2.TerminateInstancesOutput{}, nil
}

// Create a mock EC2 ModifyInstanceAttribute method
func (m mockEC2Client) ModifyInstanceAttribute(ctx context.Context,
	params *ec2.ModifyInstanceAttributeInput,
	optFns ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error) {

	// Return a mock ModifyInstanceAttributeOutput
	return &ec2.ModifyInstanceAttributeOutput{}, nil
}

// Create a mock EC2 StopInstances method
func (m mockEC2Client) StopInstances(ctx context.Context,
	params *ec2.StopInstancesInput,
	optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {

	// Return a mock StopInstancesOutput
	return &ec2.StopInstancesOutput{}, nil
}

// Create a mock EC2 StartInstances method
func (m mockEC2Client) StartInstances(ctx context.Context,
	params *ec2.StartInstancesInput,
	optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {

	// Return a mock StartInstancesOutput
	return &ec2.StartInstancesOutput{}, nil
}

// Create a serviceConfig struct without public IP
var serviceConfig = &Config{
	Region: "us-east-1",
	// Add instance type to serviceConfig
	InstanceType: "t2.small",
	// Add subnet ID to serviceConfig
	SubnetId: "subnet-1234567890abcdef0",
	// Add security group ID to serviceConfig
	SecurityGroupIds: []string{"sg-1234567890abcdef0"},
	// Add image ID to serviceConfig
	ImageId: "ami-1234567890abcdef0",
	// Add InstanceTypes to serviceConfig
	InstanceTypes: []string{"t2.small", "t2.medium"},
}

// Create a serviceConfig struct with invalid instance type
var serviceConfigInvalidInstanceType = &Config{
	Region: "us-east-1",
	// Add instance type to serviceConfig
	InstanceType: "t2.small",
	// Add subnet ID to serviceConfig
	SubnetId: "subnet-1234567890abcdef0",
	// Add security group ID to serviceConfig
	SecurityGroupIds: []string{"sg-1234567890abcdef0"},
	// Add image ID to serviceConfig
	ImageId: "ami-1234567890abcdef0",
	// Add InstanceTypes to serviceConfig
	InstanceTypes: []string{"t2.large", "t2.medium"},
}

// Create a serviceConfig with emtpy InstanceTypes
var serviceConfigEmptyInstanceTypes = &Config{
	Region: "us-east-1",
	// Add instance type to serviceConfig
	InstanceType: "t2.small",
	// Add subnet ID to serviceConfig
	SubnetId: "subnet-1234567890abcdef0",
	// Add security group ID to serviceConfig
	SecurityGroupIds: []string{"sg-1234567890abcdef0"},
	// Add image ID to serviceConfig
	ImageId: "ami-1234567890abcdef0",
	// Add InstanceTypes to serviceConfig
	InstanceTypes: []string{},
}

// Create cloud.Instance array with 5 instances
// with ID, Name and IPs net.IP array
var instances = []cloud.Instance{
	{
		ID:   "i-1234567890abcdef0",
		Name: "test-instance-1",
		IPs:  []net.IP{net.ParseIP("10.0.0.2")},
	},
	// Repeat 4 times
	{
		ID:   "i-1234567890abcdef1",
		Name: "test-instance-2",
		IPs:  []net.IP{net.ParseIP("10.0.0.3")},
	},
	{
		ID:   "i-1234567890abcdef2",
		Name: "test-instance-3",
		IPs:  []net.IP{net.ParseIP("10.0.0.4")},
	},
	{
		ID:   "i-1234567890abcdef3",
		Name: "test-instance-4",
		IPs:  []net.IP{net.ParseIP("10.0.0.5")},
	},
	{
		ID:   "i-1234567890abcdef4",
		Name: "test-instance-5",
		IPs:  []net.IP{net.ParseIP("10.0.0.6")},
	},
}

// Create a serviceConfig to initialize a pool of 5 instances
var serviceConfigPool = &Config{
	Region: "us-east-1",
	// Add instance type to serviceConfig
	InstanceType: "t2.small",
	// Add subnet ID to serviceConfig
	SubnetId: "subnet-1234567890abcdef0",
	// Add security group ID to serviceConfig
	SecurityGroupIds: []string{"sg-1234567890abcdef0"},
	// Add image ID to serviceConfig
	ImageId: "ami-1234567890abcdef0",
	// Add DesiredPoolSize to serviceConfig
	DesiredPoolSize: 5,
	// Add PreCreatedInstances to serviceConfig
	PreCreatedInstances: instances,
}

type mockCloudConfig struct{}

func (c *mockCloudConfig) Generate() (string, error) {
	return "cloud config", nil
}

func TestCreateInstance(t *testing.T) {
	type fields struct {
		ec2Client     ec2Client
		serviceConfig *Config
	}
	type args struct {
		ctx          context.Context
		podName      string
		sandboxID    string
		cloudConfig  cloudinit.CloudConfigGenerator
		instanceType string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *cloud.Instance
		wantErr bool
	}{
		// Test creating an instance
		{
			name: "CreateInstance",
			// Add fields to test
			fields: fields{
				// Add mock EC2 client to fields
				ec2Client: newMockEC2Client(),
				// Add serviceConfig to fields
				serviceConfig: serviceConfig,
			},
			args: args{
				ctx:          context.Background(),
				podName:      "podtest",
				sandboxID:    "123",
				cloudConfig:  &mockCloudConfig{},
				instanceType: "t2.small",
			},
			want: &cloud.Instance{
				ID:   "i-1234567890abcdef0",
				Name: "podvm-podtest-123",
				IPs:  []net.IP{net.ParseIP("10.0.0.2")},
			},
			// Test should not return an error
			wantErr: false,
		},
		// Test creating an instance with invalid instance type
		{
			name: "CreateInstanceInvalidInstanceType",
			// Add fields to test
			fields: fields{
				// Add mock EC2 client to fields
				ec2Client: newMockEC2Client(),
				// Add serviceConfigInvalidInstanceType to fields
				serviceConfig: serviceConfigInvalidInstanceType,
			},
			args: args{
				ctx:          context.Background(),
				podName:      "podinvalidinstance",
				sandboxID:    "123",
				cloudConfig:  &mockCloudConfig{},
				instanceType: "t2.small",
			},
			want: nil,
			// Test should return an error
			wantErr: true,
		},
		// Test creating an instance with empty InstanceTypes
		// The instance type is not set to default value
		{
			name: "CreateInstanceEmptyInstanceTypes",
			// Add fields to test
			fields: fields{
				// Add mock EC2 client to fields
				ec2Client: newMockEC2Client(),
				// Add serviceConfigEmptyInstanceTypes to fields
				serviceConfig: serviceConfigEmptyInstanceTypes,
			},
			args: args{
				ctx:          context.Background(),
				podName:      "podemptyinstance",
				sandboxID:    "123",
				cloudConfig:  &mockCloudConfig{},
				instanceType: "t2.large",
			},
			want: nil,
			// Test should return an error
			wantErr: true,
		},
		// Test creating an instance from pre-created instances pool
		{
			name: "CreateInstanceFromPreCreatedInstancesPool",
			// Add fields to test
			fields: fields{
				// Add mock EC2 client to fields
				ec2Client: newMockEC2Client(),
				// Add serviceConfigPool to fields
				serviceConfig: serviceConfigPool,
			},
			args: args{
				ctx:          context.Background(),
				podName:      "podtest",
				sandboxID:    "123",
				cloudConfig:  &mockCloudConfig{},
				instanceType: "t2.small",
			},
			want: &cloud.Instance{
				ID:   "i-1234567890abcdef0",
				Name: "podvm-podtest-123",
				IPs:  []net.IP{net.ParseIP("10.0.0.2")},
			},
			// Test should not return an error
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			p := &awsProvider{
				ec2Client:     tt.fields.ec2Client,
				serviceConfig: tt.fields.serviceConfig,
			}

			got, err := p.CreateInstance(tt.args.ctx, tt.args.podName, tt.args.sandboxID, tt.args.cloudConfig, tt.args.instanceType)
			if (err != nil) != tt.wantErr {
				t.Errorf("awsProvider.CreateInstance() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("awsProvider.CreateInstance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteInstance(t *testing.T) {
	type fields struct {
		ec2Client     ec2Client
		serviceConfig *Config
	}
	type args struct {
		ctx        context.Context
		instanceID string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// Test deleting an instance
		{
			name: "DeleteInstance",
			fields: fields{
				ec2Client:     newMockEC2Client(),
				serviceConfig: serviceConfig,
			},
			args: args{
				ctx:        context.Background(),
				instanceID: "i-1234567890abcdef0",
			},
			// Test should not return an error
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &awsProvider{
				ec2Client:     tt.fields.ec2Client,
				serviceConfig: tt.fields.serviceConfig,
			}
			if err := p.DeleteInstance(tt.args.ctx, tt.args.instanceID); (err != nil) != tt.wantErr {
				t.Errorf("awsProvider.DeleteInstance() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInitializePodVmPool(t *testing.T) {
	type fields struct {
		ec2Client     ec2Client
		serviceConfig *Config
	}
	type args struct {
		ctx          context.Context
		numInstances int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// Create a pool of 5 instances
		{
			name: "InitializePodVmPool",
			fields: fields{
				ec2Client:     newMockEC2Client(),
				serviceConfig: serviceConfigPool,
			},
			args: args{
				ctx:          context.Background(),
				numInstances: serviceConfig.DesiredPoolSize,
			},
			// Test should not return an error
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &awsProvider{
				ec2Client:     tt.fields.ec2Client,
				serviceConfig: tt.fields.serviceConfig,
			}
			if err := p.initializePodVmPool(tt.args.ctx, tt.args.numInstances); (err != nil) != tt.wantErr {
				t.Errorf("awsProvider.initializePodVmPool() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
