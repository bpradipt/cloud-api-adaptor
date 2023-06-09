package util

import (
	"fmt"
	"strings"

	cri "github.com/containerd/containerd/pkg/cri/annotations"
)

const (
	podvmNamePrefix = "podvm"
	// Defined in webhook/pkg/mutating_webhook/remove-resourcespec.go
	POD_VM_ANNOTATION_INSTANCE_TYPE = "kata.peerpods.io/instance_type"
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
func GetInstanceType(annotations map[string]string) string {
	return annotations[POD_VM_ANNOTATION_INSTANCE_TYPE]
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
