// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package byom

import (
	"context"
	"encoding/json"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestConfigMapVMPoolManagerRecoverState(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()
	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Pre-create ConfigMap with some state
	existingState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"test-allocation-1": {
				AllocationID: "test-allocation-1",
				IP:           "192.168.1.10",
				AllocatedAt:  metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.11", "192.168.1.12"},
		LastUpdated:  metav1.Now(),
		Version:      1,
	}

	stateData, _ := json.Marshal(existingState)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.Namespace,
		},
		Data: map[string]string{
			stateDataKey: string(stateData),
		},
	}

	_, err := client.CoreV1().ConfigMaps(config.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Test state recovery
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify state was recovered correctly
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2, got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1, got %d", inUse)
	}

	// Verify specific allocation exists
	allocatedIP, exists, err := manager.GetAllocatedIP(ctx, "test-allocation-1")
	if err != nil {
		t.Errorf("Failed to get allocated IP: %v", err)
	}

	if !exists {
		t.Error("Expected allocation to exist after recovery")
	}

	if allocatedIP.String() != "192.168.1.10" {
		t.Errorf("Expected allocated IP 192.168.1.10, got %s", allocatedIP.String())
	}
}

func TestConfigMapVMPoolManagerRecoverStateWithNodeAllocations(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Create ConfigMap with allocations including some from the current test node
	existingState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"other-node-allocation": {
				AllocationID:  "other-node-allocation",
				IP:            "192.168.1.10",
				NodeName:      "other-node",
				PodName:       "other-pod",
				PodNamespace:  "other-namespace",
				AllocatedAt:   metav1.Now(),
			},
			"test-node-allocation": {
				AllocationID:  "test-node-allocation",
				IP:            "192.168.1.11",
				NodeName:      "test-node", // This should be released
				PodName:       "test-pod",
				PodNamespace:  "test-namespace",
				AllocatedAt:   metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.12"},
		LastUpdated:  metav1.Now(),
		Version:      1,
	}

	stateData, _ := json.Marshal(existingState)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.Namespace,
		},
		Data: map[string]string{
			stateDataKey: string(stateData),
		},
	}

	_, err := client.CoreV1().ConfigMaps(config.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Track VM cleanup calls
	cleanupCalled := false
	vmCleanupFunc := func(ctx context.Context, ip netip.Addr) error {
		if ip.String() == "192.168.1.11" {
			cleanupCalled = true
		}
		return nil // Simulate successful cleanup
	}

	// Test state recovery with VM cleanup
	err = manager.RecoverState(ctx, vmCleanupFunc)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	if !cleanupCalled {
		t.Error("Expected VM cleanup to be called for test-node allocation")
	}

	// Verify state after recovery
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	// Should have: other-node allocation (1) + available IPs (2)
	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2 (test-node IP released), got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1 (other-node allocation kept), got %d", inUse)
	}

	// Verify test-node allocation was released
	_, exists, err := manager.GetAllocatedIP(ctx, "test-node-allocation")
	if err != nil {
		t.Errorf("Failed to check test-node allocation: %v", err)
	}
	if exists {
		t.Error("Expected test-node allocation to be released")
	}

	// Verify other-node allocation was kept
	_, exists, err = manager.GetAllocatedIP(ctx, "other-node-allocation")
	if err != nil {
		t.Errorf("Failed to check other-node allocation: %v", err)
	}
	if !exists {
		t.Error("Expected other-node allocation to be kept")
	}
}

func TestConfigMapVMPoolManagerRecoverStateWithFailedCleanup(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Create ConfigMap with test-node allocation
	existingState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"test-node-allocation": {
				AllocationID:  "test-node-allocation",
				IP:            "192.168.1.10",
				NodeName:      "test-node",
				PodName:       "test-pod",
				PodNamespace:  "test-namespace",
				AllocatedAt:   metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.11", "192.168.1.12"},
		LastUpdated:  metav1.Now(),
		Version:      1,
	}

	stateData, _ := json.Marshal(existingState)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.Namespace,
		},
		Data: map[string]string{
			stateDataKey: string(stateData),
		},
	}

	_, err := client.CoreV1().ConfigMaps(config.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Simulate failed VM cleanup
	vmCleanupFunc := func(ctx context.Context, ip netip.Addr) error {
		return fmt.Errorf("simulated cleanup failure for %s", ip.String())
	}

	// Test state recovery with failed cleanup
	err = manager.RecoverState(ctx, vmCleanupFunc)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify IP with failed cleanup remains allocated
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2 (failed cleanup IP kept allocated), got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1 (failed cleanup IP kept allocated), got %d", inUse)
	}

	// Verify allocation still exists due to failed cleanup
	_, exists, err := manager.GetAllocatedIP(ctx, "test-node-allocation")
	if err != nil {
		t.Errorf("Failed to check allocation: %v", err)
	}
	if !exists {
		t.Error("Expected allocation to remain due to failed cleanup")
	}
}

func TestConfigMapVMPoolManagerRecoverEmptyState(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// No ConfigMap exists
	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Test state recovery (should initialize empty state)
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify empty state was initialized
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 2 {
		t.Errorf("Expected total 2, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2, got %d", available)
	}

	if inUse != 0 {
		t.Errorf("Expected inUse 0, got %d", inUse)
	}
}

func TestConfigMapVMPoolManagerRepairStateFromPrimaryConfig(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Create ConfigMap with state that doesn't match primary config
	corruptedState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"valid-allocation": {
				AllocationID:  "valid-allocation",
				IP:            "192.168.1.10", // Valid IP from primary config
				NodeName:      "other-node",
				PodName:       "valid-pod",
				PodNamespace:  "valid-namespace",
				AllocatedAt:   metav1.Now(),
			},
			"invalid-allocation": {
				AllocationID:  "invalid-allocation",
				IP:            "10.0.0.1", // Invalid IP (not in primary config)
				NodeName:      "other-node",
				PodName:       "invalid-pod",
				PodNamespace:  "invalid-namespace",
				AllocatedAt:   metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.11", "10.0.0.2", "192.168.1.11"}, // Mix of valid, invalid, and duplicate
		LastUpdated:  metav1.Now(),
		Version:      1,
	}

	stateData, _ := json.Marshal(corruptedState)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.Namespace,
		},
		Data: map[string]string{
			stateDataKey: string(stateData),
		},
	}

	_, err := client.CoreV1().ConfigMaps(config.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Test state recovery - should repair to match primary config
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify state was repaired to match primary config
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3 (primary config size), got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2 (primary config - valid allocation), got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1 (only valid allocation kept), got %d", inUse)
	}

	// Verify only valid allocation remains
	allocatedIPs, err := manager.ListAllocatedIPs(ctx)
	if err != nil {
		t.Errorf("Failed to list allocated IPs: %v", err)
	}

	if len(allocatedIPs) != 1 {
		t.Errorf("Expected 1 allocated IP after repair, got %d", len(allocatedIPs))
	}

	// Invalid allocation should be removed
	_, exists := allocatedIPs["invalid-allocation"]
	if exists {
		t.Error("Expected invalid allocation to be removed")
	}

	// Valid allocation should remain
	validAllocation, exists := allocatedIPs["valid-allocation"]
	if !exists {
		t.Error("Expected valid allocation to remain")
	}

	if validAllocation.IP != "192.168.1.10" {
		t.Errorf("Expected valid allocation IP 192.168.1.10, got %s", validAllocation.IP)
	}

	// Verify available IPs by checking we can allocate the remaining IPs
	// Since 192.168.1.10 is allocated, we should be able to allocate 192.168.1.11 and 192.168.1.12
	ip1, err := manager.AllocateIP(ctx, "test-alloc-1", "test-pod-1", "test-ns")
	if err != nil {
		t.Errorf("Failed to allocate first available IP: %v", err)
	}

	ip2, err := manager.AllocateIP(ctx, "test-alloc-2", "test-pod-2", "test-ns")
	if err != nil {
		t.Errorf("Failed to allocate second available IP: %v", err)
	}

	// Verify the allocated IPs are from the primary config
	expectedIPs := map[string]bool{
		"192.168.1.11": true,
		"192.168.1.12": true,
	}

	if !expectedIPs[ip1.String()] {
		t.Errorf("Allocated IP %s not in expected available IPs", ip1.String())
	}

	if !expectedIPs[ip2.String()] {
		t.Errorf("Allocated IP %s not in expected available IPs", ip2.String())
	}

	if ip1.String() == ip2.String() {
		t.Error("Expected different IPs to be allocated")
	}
}

func TestConfigMapVMPoolManagerPoolIPsChange(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Start with original pool configuration
	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Create ConfigMap with existing state using original pool IPs
	existingState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"test-allocation-1": {
				AllocationID: "test-allocation-1",
				IP:           "192.168.1.10", // Will remain valid
				AllocatedAt:  metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.11", "192.168.1.12"}, // 192.168.1.11 will become invalid
		LastUpdated:  metav1.Now(),
		Version:      1,
	}

	stateData, _ := json.Marshal(existingState)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.Namespace,
		},
		Data: map[string]string{
			stateDataKey: string(stateData),
		},
	}

	_, err := client.CoreV1().ConfigMaps(config.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	// Now change the pool IPs (same count: 3->3, but different IPs)
	// This simulates the scenario where PoolIPs are updated in configuration
	config.PoolIPs = []string{"192.168.1.10", "192.168.1.13", "192.168.1.14"} // Replaced .11 and .12 with .13 and .14

	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Test state recovery - this should detect the PoolIP changes and update ConfigMap
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify that the state was updated to reflect new pool configuration
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2, got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1, got %d", inUse)
	}

	// Verify that the ConfigMap was actually updated with new state
	updatedCM, err := client.CoreV1().ConfigMaps(config.Namespace).Get(ctx, config.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get updated ConfigMap: %v", err)
	}

	var updatedState IPAllocationState
	err = json.Unmarshal([]byte(updatedCM.Data[stateDataKey]), &updatedState)
	if err != nil {
		t.Fatalf("Failed to unmarshal updated state: %v", err)
	}

	// Verify the version was incremented (proves ConfigMap was updated)
	if updatedState.Version <= existingState.Version {
		t.Errorf("Expected version to be incremented from %d, got %d", existingState.Version, updatedState.Version)
	}

	// Verify available IPs now contain the new pool IPs (192.168.1.13, 192.168.1.14)
	expectedAvailable := []string{"192.168.1.13", "192.168.1.14"}
	if len(updatedState.AvailableIPs) != len(expectedAvailable) {
		t.Errorf("Expected %d available IPs, got %d", len(expectedAvailable), len(updatedState.AvailableIPs))
	}

	for _, expectedIP := range expectedAvailable {
		found := false
		for _, actualIP := range updatedState.AvailableIPs {
			if actualIP == expectedIP {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected available IP %s not found in updated state", expectedIP)
		}
	}

	// Verify that the valid allocation (192.168.1.10) was preserved
	if len(updatedState.AllocatedIPs) != 1 {
		t.Errorf("Expected 1 allocated IP, got %d", len(updatedState.AllocatedIPs))
	}

	allocation, exists := updatedState.AllocatedIPs["test-allocation-1"]
	if !exists {
		t.Error("Expected valid allocation to be preserved")
	}

	if allocation.IP != "192.168.1.10" {
		t.Errorf("Expected preserved allocation IP 192.168.1.10, got %s", allocation.IP)
	}
}
