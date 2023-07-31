// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"
	"testing"
)

func TestAWSMasking(t *testing.T) {
	secretKey := "abcdefg"
	region := "eu-gb"
	cloudCfg := Config{
		SecretKey: secretKey,
		Region:    region,
	}
	checkLine := func(verb string) {
		logline := fmt.Sprintf(verb, cloudCfg.Redact())
		if strings.Contains(logline, secretKey) {
			t.Errorf("For verb %s: %s contains the secret key: %s", verb, logline, secretKey)
		}
		if !strings.Contains(logline, region) {
			t.Errorf("For verb %s: %s doesn't contain the region name: %s", verb, logline, region)
		}
	}
	checkLine("%v")
	checkLine("%s")

	if cloudCfg.SecretKey != secretKey {
		t.Errorf("Original SecretKey field value has been overwritten")
	}
}

func TestEmptyList(t *testing.T) {
	var list instanceTypes
	err := list.Set("")
	if err != nil {
		t.Errorf("List Set failed, %v", err)
	}
	if len(list) != 0 {
		t.Errorf("Expect 0 length, got %d", len(list))
	}
}

func TestEmptyKeyValueFlag_Set(t *testing.T) {
	// Empty KeyValueFlag will result in error
	var flag keyValueFlag
	err := flag.Set("")
	if err == nil {
		t.Errorf("Expect error, got nil")
	}
}

func TestKeyValueFlag_Set(t *testing.T) {
	tests := []struct {
		// Add test name
		name          string
		input         string
		expectedValue keyValueFlag
		expectedError bool
	}{
		{
			name:  "valid key value pair",
			input: "key1=value1,key2=value2,key3=value3",
			expectedValue: keyValueFlag{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
			expectedError: false,
		},
		{
			name:          "invalid key value pair",
			input:         "invalid",
			expectedValue: nil,
			expectedError: true,
		},
		// Add more test cases "key1=value1, key2=value2" and "key1=value1,key2=value2, key3=value3"
		// to cover all the cases
		{
			name:  "valid key value pair with spaces 1",
			input: "key1=value1, key2=value2",
			expectedValue: keyValueFlag{
				"key1": "value1",
				"key2": "value2",
			},
			expectedError: false,
		},
		{
			name:  "valid key value pair with spaces 2",
			input: "key1=value1,key2=value2, key3=value3",
			expectedValue: keyValueFlag{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
			expectedError: false,
		},
		// Add test case for "key1=value1 key2=value2"
		{
			name:  "invalid key value pair separated with spaces",
			input: "key1=value1 key2=value2",
			expectedValue: keyValueFlag{
				"key1": "value1 key2=value2",
			},
			expectedError: false,
		},
	}

	for _, test := range tests {
		// Create a new KeyValueFlag
		k := make(keyValueFlag)

		// Set the flag value
		err := k.Set(test.input)

		// Check the result
		if (err != nil) != test.expectedError {

			t.Errorf("Unexpected error for test ('%s'), got: %v, expected error: %v, received value: %v", test.name, err, test.expectedError, k)
		}

		if test.expectedError {
			continue
		}

		// Compare the KeyValueFlag value
		if !isEqual(k, test.expectedValue) {
			t.Errorf("Unexpected KeyValueFlag value, got: %v, expected: %v", k, test.expectedValue)
		}
	}
}

func isEqual(a, b keyValueFlag) bool {
	if len(a) != len(b) {
		return false
	}

	for key, value := range a {
		if bValue, ok := b[key]; !ok || value != bValue {
			return false
		}
	}

	return true
}
