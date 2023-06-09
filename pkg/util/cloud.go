package util

import (
	"fmt"
	"strconv"
	"strings"

	cri "github.com/containerd/containerd/pkg/cri/annotations"
	hypannotations "github.com/kata-containers/kata-containers/src/runtime/virtcontainers/pkg/annotations"
)

const (
	podvmNamePrefix = "podvm"
)

func sanitize(input string) string {

	var output string

	for _, c := range strings.ToLower(input) {
		if !(('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-') {
			c = '-'
		}
		output += string(c)
	}

	return output
}

func GenerateInstanceName(podName, sandboxID string, podvmNameMax int) string {

	podName = sanitize(podName)
	sandboxID = sanitize(sandboxID)

	prefixLen := len(podvmNamePrefix)
	podNameLen := len(podName)
	if podvmNameMax > 0 && prefixLen+podNameLen+10 > podvmNameMax {
		podNameLen = podvmNameMax - prefixLen - 10
		if podNameLen < 0 {
			panic(fmt.Errorf("podvmNameMax is too small: %d", podvmNameMax))
		}
		fmt.Printf("podNameLen: %d", podNameLen)
	}

	instanceName := fmt.Sprintf("%s-%.*s-%.8s", podvmNamePrefix, podNameLen, podName, sandboxID)

	return instanceName
}

func GetPodName(annotations map[string]string) string {

	sandboxName := annotations[cri.SandboxName]

	// cri-o stores the sandbox name in the form of k8s_<pod name>_<namespace>_<uid>_0
	// Extract the pod name from it.
	if tmp := strings.Split(sandboxName, "_"); len(tmp) > 1 && tmp[0] == "k8s" {
		return tmp[1]
	}

	return sandboxName
}

func GetPodNamespace(annotations map[string]string) string {

	return annotations[cri.SandboxNamespace]
}

// Method to get instance type from annotations
func GetInstanceTypeFromAnnotation(annotations map[string]string) string {
	return annotations[hypannotations.MachineType]
}

// Method to get vCPU and memory from annotations
func GetCPUAndMemoryFromAnnotation(annotations map[string]string) (int64, int64) {

	var vcpuInt, memoryInt int64
	var err error

	vcpu, ok := annotations[hypannotations.DefaultVCPUs]
	if ok {
		vcpuInt, err = strconv.ParseInt(vcpu, 10, 64)
		if err != nil {
			fmt.Printf("Error converting vcpu to int64. Defaulting to 0: %v\n", err)
			vcpuInt = 0
		}
	} else {
		vcpuInt = 0
	}

	memory, ok := annotations[hypannotations.DefaultMemory]
	if ok {
		// Use strconv.ParseInt to convert string to int64
		memoryInt, err = strconv.ParseInt(memory, 10, 64)
		if err != nil {
			fmt.Printf("Error converting memory to int64. Defaulting to 0: %v\n", err)
			memoryInt = 0
		}

	} else {
		memoryInt = 0
	}

	// Return vCPU and memory
	return vcpuInt, memoryInt
}

// Method to check if a string exists in a slice
func Contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// Method to verify the correct instanceType to be used for Pod VM
func VerifyCloudInstanceType(instanceType string, instanceTypes []string, defaultInstanceType string) (string, error) {
	// If instanceType is empty, set instanceType to default. Non-empty instanceType is set via annotation
	if instanceType == "" {
		instanceType = defaultInstanceType
		fmt.Printf("Using default instance type (%q)\n", defaultInstanceType)
		return instanceType, nil
	}

	// If instanceTypes is empty and instanceType is not default, return error
	if len(instanceTypes) == 0 && instanceType != defaultInstanceType {
		// Return error if instanceTypes is empty and instanceType is not default
		return "", fmt.Errorf("requested instance type (%q) is not default (%q) and supported instance types list is empty",
			instanceType, defaultInstanceType)

	}

	// If instanceTypes is not empty and instanceType is not among the supported instance types, return error
	if len(instanceTypes) > 0 && !Contains(instanceTypes, instanceType) {
		return "", fmt.Errorf("requested instance type (%q) is not part of supported instance types list", instanceType)
	}

	return instanceType, nil
}
