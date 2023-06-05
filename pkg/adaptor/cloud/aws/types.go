// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"errors"
	"fmt"
	"strings"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/cloud"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util"
)

type securityGroupIds []string

func (i *securityGroupIds) String() string {
	return strings.Join(*i, ", ")
}

func (i *securityGroupIds) Set(value string) error {
	*i = append(*i, strings.Split(value, ",")...)
	return nil
}

type instanceTypes []string

func (i *instanceTypes) String() string {
	return strings.Join(*i, ", ")
}

func (i *instanceTypes) Set(value string) error {
	*i = append(*i, strings.Split(value, ",")...)
	return nil
}

// keyValueFlag represents a flag of key-value pairs
type keyValueFlag map[string]string

// String returns the string representation of the keyValueFlag
func (k *keyValueFlag) String() string {
	var pairs []string
	for key, value := range *k {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, value))
	}
	return strings.Join(pairs, ", ")
}

// Set parses the input string and sets the keyValueFlag value
func (k *keyValueFlag) Set(value string) error {
	pairs := strings.Split(value, ",")
	for _, pair := range pairs {
		keyValue := strings.SplitN(pair, "=", 2)
		if len(keyValue) != 2 {
			return errors.New("invalid key-value pair: " + pair)
		}
		key := strings.TrimSpace(keyValue[0])
		value := strings.TrimSpace(keyValue[1])
		(*k)[key] = value
	}
	return nil
}

type Config struct {
	AccessKeyId        string
	SecretKey          string
	Region             string
	LoginProfile       string
	LaunchTemplateName string
	ImageId            string
	InstanceType       string
	KeyName            string
	SubnetId           string
	SecurityGroupIds   securityGroupIds
	UseLaunchTemplate  bool
	UsePublicIP        bool
	InstanceTypes      instanceTypes
	Tags               keyValueFlag
	DesiredPoolSize    int
	// Add cloud.Instance array to store the precreated instances
	PreCreatedInstances []cloud.Instance
}

func (c Config) Redact() Config {
	return *util.RedactStruct(&c, "AccessKeyId", "SecretKey").(*Config)
}
