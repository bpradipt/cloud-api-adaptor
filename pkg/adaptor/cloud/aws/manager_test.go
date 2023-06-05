// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"flag"
	"reflect"
	"testing"
)

func TestManager_ParseCmd(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected Config
	}{
		{
			name: "AllFlagsSet",
			args: []string{
				"-aws-access-key-id=test-access-key",
				"-aws-secret-key=test-secret-key",
				"-aws-region=test-region",
				"-aws-profile=test-profile",
				"-aws-lt-name=test-lt-name",
				"-use-lt=true",
				"-imageid=test-image-id",
				"-instance-type=test-instance-type",
				"-securitygroupids=sg-1,sg-2",
				"-keyname=test-key-name",
				"-subnetid=test-subnet-id",
				"-use-public-ip=true",
				"-instance-types=t2.micro,t3.small",
				"-tags=key1=value1,key2=value2",
				"-desired-pool-size=5",
			},
			expected: Config{
				AccessKeyId:        "test-access-key",
				SecretKey:          "test-secret-key",
				Region:             "test-region",
				LoginProfile:       "test-profile",
				LaunchTemplateName: "test-lt-name",
				UseLaunchTemplate:  true,
				ImageId:            "test-image-id",
				InstanceType:       "test-instance-type",
				SecurityGroupIds:   []string{"sg-1", "sg-2"},
				KeyName:            "test-key-name",
				SubnetId:           "test-subnet-id",
				UsePublicIP:        true,
				InstanceTypes:      []string{"t2.micro", "t3.small"},
				Tags:               map[string]string{"key1": "value1", "key2": "value2"},
				DesiredPoolSize:    5,
			},
		},
		{
			name: "DefaultValues",
			args: []string{},
			expected: Config{
				AccessKeyId:        "",
				SecretKey:          "",
				Region:             "",
				LoginProfile:       "test",
				LaunchTemplateName: "kata",
				UseLaunchTemplate:  false,
				ImageId:            "",
				InstanceType:       "t3.small",
				SecurityGroupIds:   securityGroupIds{},
				KeyName:            "",
				SubnetId:           "",
				UsePublicIP:        false,
				InstanceTypes:      instanceTypes{},
				Tags:               nil,
				DesiredPoolSize:    0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create a new flag set
			flags := flag.NewFlagSet("test", flag.ContinueOnError)

			// Create a new Manager instance
			manager := &Manager{}

			// Parse the command-line flags
			manager.ParseCmd(flags)

			// Set the command-line arguments
			flags.Parse(test.args)

			// Check if the expected values match the actual values
			if !reflect.DeepEqual(awscfg, test.expected) {
				t.Errorf("Expected config: %+v, but got: %+v", test.expected, awscfg)
			}
		})
	}
}
