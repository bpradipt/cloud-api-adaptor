// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package byom

import (
	"context"
	"fmt"
	"net/netip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoverState initializes state from persistent storage
// Primary: ConfigMap with node-specific cleanup, Fallback: Initialize empty state
func (cm *ConfigMapVMPoolManager) RecoverState(ctx context.Context, vmCleanupFunc func(context.Context, netip.Addr) error) error {
	// Lock the entire recovery process to prevent concurrent allocation interference
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	logger.Printf("Starting state recovery for VM pool...")

	// Get current node name
	currentNode, err := getCurrentNodeName()
	if err != nil {
		return fmt.Errorf("failed to get current node name: %w", err)
	}
	logger.Printf("CAA running on node: %s", currentNode)

	// Try to recover from ConfigMap first
	state, _, err := cm.getCurrentState(ctx)
	if err == nil && state != nil {
		// ConfigMap exists and is valid
		total := len(state.AllocatedIPs) + len(state.AvailableIPs)
		logger.Printf("State recovered from ConfigMap: %d total IPs, %d allocated, %d available",
			total, len(state.AllocatedIPs), len(state.AvailableIPs))

		// Log node allocations but do NOT release them - let PeerPod controller handle cleanup
		nodeAllocations := 0
		for _, allocation := range state.AllocatedIPs {
			if allocation.NodeName == currentNode {
				nodeAllocations++
				logger.Printf("Found allocation on current node %s: IP=%s, Pod=%s/%s",
					currentNode, allocation.IP, allocation.PodNamespace, allocation.PodName)
			}
		}
		logger.Printf("Current node %s has %d allocations - will be cleaned by PeerPod controller", currentNode, nodeAllocations)

		// Only repair state to match primary configuration (keep all allocations)
		if err := cm.repairStateFromPrimaryConfig(ctx); err != nil {
			logger.Printf("Warning: failed to repair state from primary config: %v", err)
		}

		return nil
	}

	// ConfigMap doesn't exist or is corrupted, initialize empty state
	logger.Printf("ConfigMap state not available, initializing empty state")
	return cm.initializeAndSaveEmptyState(ctx)
}


// repairStateFromPrimaryConfig rebuilds the state to match the primary configuration from peer-pods-cm
// AvailableIPs = config.PoolIPs - currently allocated IPs (keeps ALL allocations for PeerPod controller cleanup)
func (cm *ConfigMapVMPoolManager) repairStateFromPrimaryConfig(ctx context.Context) error {
	// Get current state
	currentState, _, err := cm.getCurrentState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current state for repair: %w", err)
	}

	// Keep ALL existing allocations - let PeerPod controller handle cleanup of orphaned pods
	// This prevents race conditions during CAA restart
	validAllocatedIPs := currentState.AllocatedIPs

	// Build AvailableIPs = primary config - all allocated IPs
	allocatedIPSet := make(map[string]bool)
	for _, allocation := range validAllocatedIPs {
		allocatedIPSet[allocation.IP] = true
	}

	availableIPs := []string{}
	for _, ip := range cm.config.PoolIPs {
		if !allocatedIPSet[ip] {
			availableIPs = append(availableIPs, ip)
		}
	}

	// Update state with repaired configuration
	repairedState := &IPAllocationState{
		AllocatedIPs: validAllocatedIPs, // Keep all allocations unchanged
		AvailableIPs: availableIPs,
		LastUpdated:  metav1.Now(),
		Version:      currentState.Version + 1,
	}

	logger.Printf("Repairing state: primary config has %d IPs, keeping %d allocated (including orphaned), %d available",
		len(cm.config.PoolIPs), len(validAllocatedIPs), len(availableIPs))

	// Atomic update - handles concurrent CAA instances
	if err := cm.updateState(ctx, repairedState); err != nil {
		return fmt.Errorf("failed to update repaired state: %w", err)
	}

	logger.Printf("State successfully repaired to match primary configuration - PeerPod controller will handle orphaned allocations")
	return nil
}

// initializeEmptyState creates an empty state with all IPs available
func (cm *ConfigMapVMPoolManager) initializeEmptyState() *IPAllocationState {
	return &IPAllocationState{
		AllocatedIPs: make(map[string]IPAllocation),
		AvailableIPs: append([]string{}, cm.config.PoolIPs...), // Copy slice
		LastUpdated:  metav1.Now(),
		Version:      1,
	}
}

// initializeAndSaveEmptyState creates and saves an empty state
func (cm *ConfigMapVMPoolManager) initializeAndSaveEmptyState(ctx context.Context) error {
	emptyState := cm.initializeEmptyState()

	if err := cm.updateState(ctx, emptyState); err != nil {
		return fmt.Errorf("failed to initialize empty state: %w", err)
	}

	logger.Printf("Initialized empty state with %d available IPs", len(emptyState.AvailableIPs))
	return nil
}
