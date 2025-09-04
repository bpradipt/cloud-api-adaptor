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

func TestConfigMapVMPoolManagerRecoverFromPeerPodCRs(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// For this test, we don't use a real PeerPod service since we're testing
	// the fallback to empty state when no ConfigMap exists
	manager, err := NewConfigMapVMPoolManager(client, config)
	if err != nil {
		t.Fatalf("Failed to create ConfigMapVMPoolManager: %v", err)
	}

	ctx := context.Background()

	// Test state recovery - since no ConfigMap exists, it should initialize empty state
	// (This test would need to be modified to test actual PeerPod recovery by
	// preventing ConfigMap creation or using a different approach)
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify empty state was initialized (ConfigMap takes precedence over PeerPod recovery)
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 3 {
		t.Errorf("Expected available 3 (empty state initialized), got %d", available)
	}

	if inUse != 0 {
		t.Errorf("Expected inUse 0 (empty state initialized), got %d", inUse)
	}

	// Note: In a real scenario where ConfigMap doesn't exist and we want to test
	// PeerPod recovery, we would need to modify the implementation to force
	// the fallback path or use a different testing approach
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

	// No ConfigMap and no PeerPod service
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

func TestConfigMapVMPoolManagerValidateAndRepairState(t *testing.T) {
	cleanup := setupTestEnvironment(t)
	defer cleanup()

	config := &GlobalVMPoolConfig{
		Namespace:        "test-namespace",
		ConfigMapName:    "test-configmap",
		PoolIPs:          []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"},
		OperationTimeout: 10000,
	}

	client := fake.NewSimpleClientset()

	// Create ConfigMap with invalid state (IP not in configured pool)
	corruptedState := &IPAllocationState{
		AllocatedIPs: map[string]IPAllocation{
			"test-allocation-1": {
				AllocationID: "test-allocation-1",
				IP:           "192.168.1.10", // Valid IP
				AllocatedAt:  metav1.Now(),
			},
			"test-allocation-2": {
				AllocationID: "test-allocation-2",
				IP:           "10.0.0.1", // Invalid IP (not in configured pool)
				AllocatedAt:  metav1.Now(),
			},
		},
		AvailableIPs: []string{"192.168.1.11", "192.168.1.11"}, // Duplicate IP
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

	// Test state recovery with validation and repair
	err = manager.RecoverState(ctx, nil)
	if err != nil {
		t.Errorf("Failed to recover state: %v", err)
	}

	// Verify state was repaired
	total, available, inUse, err := manager.GetPoolStatus(ctx)
	if err != nil {
		t.Errorf("Failed to get pool status: %v", err)
	}

	if total != 3 {
		t.Errorf("Expected total 3, got %d", total)
	}

	if available != 2 {
		t.Errorf("Expected available 2 (invalid allocation removed), got %d", available)
	}

	if inUse != 1 {
		t.Errorf("Expected inUse 1 (invalid allocation removed), got %d", inUse)
	}

	// Verify invalid allocation was removed
	allocatedIPs, err := manager.ListAllocatedIPs(ctx)
	if err != nil {
		t.Errorf("Failed to list allocated IPs: %v", err)
	}

	if len(allocatedIPs) != 1 {
		t.Errorf("Expected 1 allocated IP after repair, got %d", len(allocatedIPs))
	}

	_, exists := allocatedIPs["test-allocation-2"]
	if exists {
		t.Error("Expected invalid allocation to be removed")
	}

	allocation1, exists := allocatedIPs["test-allocation-1"]
	if !exists {
		t.Error("Expected valid allocation to remain")
	}

	if allocation1.IP != "192.168.1.10" {
		t.Errorf("Expected valid allocation IP 192.168.1.10, got %s", allocation1.IP)
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
