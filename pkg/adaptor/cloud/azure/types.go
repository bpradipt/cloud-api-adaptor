// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"errors"
	"fmt"
	"strings"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/util"
)

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
	SubscriptionId    string
	ClientId          string
	ClientSecret      string
	TenantId          string
	ResourceGroupName string
	Zone              string
	Region            string
	SubnetId          string
	SecurityGroupName string
	SecurityGroupId   string
	Size              string
	ImageId           string
	SSHKeyPath        string
	SSHUserName       string
	DisableCVM        bool
	Tags              keyValueFlag
}

func (c Config) Redact() Config {
	return *util.RedactStruct(&c, "ClientId", "TenantId", "ClientSecret").(*Config)
}
