// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package byom

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"reflect"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoverState initializes state from persistent storage
// Primary: ConfigMap with node-specific cleanup, Fallback: Initialize empty state
func (cm *ConfigMapVMPoolManager) RecoverState(ctx context.Context, vmCleanupFunc func(netip.Addr) error) error {
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

		// Release allocations for current node (they're now invalid due to CAA restart)
		if err := cm.releaseNodeAllocations(ctx, state, currentNode, vmCleanupFunc); err != nil {
			logger.Printf("Warning: failed to release node allocations: %v", err)
			// Continue with validation even if release fails
		}

		// Re-fetch state after potential node cleanup
		state, _, err = cm.getCurrentState(ctx)
		if err != nil {
			logger.Printf("Warning: failed to re-fetch state after node cleanup: %v", err)
		}

		return cm.validateAndRepairState(ctx, state)
	}

	// ConfigMap doesn't exist or is corrupted, initialize empty state
	logger.Printf("ConfigMap state not available, initializing empty state")
	return cm.initializeAndSaveEmptyState(ctx)
}

// validateAndRepairState validates the recovered state and repairs inconsistencies
func (cm *ConfigMapVMPoolManager) validateAndRepairState(ctx context.Context, state *IPAllocationState) error {
	// 1. Start with a complete set of all configured IPs. This is our source of truth.
	configuredIPs := make(map[string]struct{})
	for _, ip := range cm.config.PoolIPs {
		// We only add valid IPs from config to prevent bad config from polluting the state.
		if _, err := netip.ParseAddr(ip); err == nil {
			configuredIPs[ip] = struct{}{}
		} else {
			log.Printf("Warning: Invalid IP address in configuration, skipping: %s", ip)
		}
	}

	// 2. Build the new, valid "allocated" map by filtering the current state.
	// Any IP not in our configured set is automatically removed.
	repairedAllocatedIPs := make(map[string]IPAllocation)
	for allocID, allocation := range state.AllocatedIPs {
		if _, exists := configuredIPs[allocation.IP]; exists {
			repairedAllocatedIPs[allocID] = allocation
			// Remove from the set; what's left will become the available pool.
			delete(configuredIPs, allocation.IP)
		} else {
			log.Printf("Warning: Allocated IP %s is not in the configured pool; removing.", allocation.IP)
		}
	}

	// 3. Whatever remains in the configuredIPs set is the new available pool.
	repairedAvailableIPs := make([]string, 0, len(configuredIPs))
	for ip := range configuredIPs {
		repairedAvailableIPs = append(repairedAvailableIPs, ip)
	}
	// Sort for consistent ordering, which is crucial for comparison and determinism.
	sort.Strings(repairedAvailableIPs)

	// 4. Use reflect.DeepEqual for robust, idiomatic change detection.
	// We must also sort the original available IPs to ensure a fair comparison.
	sortedOriginalAvailableIPs := make([]string, len(state.AvailableIPs))
	copy(sortedOriginalAvailableIPs, state.AvailableIPs)
	sort.Strings(sortedOriginalAvailableIPs)

	if reflect.DeepEqual(repairedAllocatedIPs, state.AllocatedIPs) &&
		reflect.DeepEqual(repairedAvailableIPs, sortedOriginalAvailableIPs) {
		log.Printf("State validation completed successfully, no changes needed.")
		return nil
	}

	// 5. If changes were detected, build and persist the new state.
	log.Printf("State repairs made: allocated %d->%d, available %d->%d",
		len(state.AllocatedIPs), len(repairedAllocatedIPs),
		len(state.AvailableIPs), len(repairedAvailableIPs))

	repairedState := &IPAllocationState{
		AllocatedIPs: repairedAllocatedIPs,
		AvailableIPs: repairedAvailableIPs,
		LastUpdated:  metav1.Now(),
		Version:      state.Version + 1,
	}

	return cm.updateState(ctx, repairedState)
}

// releaseNodeAllocations releases all IP allocations for a specific node with VM cleanup support
func (cm *ConfigMapVMPoolManager) releaseNodeAllocations(ctx context.Context, state *IPAllocationState, nodeName string, vmCleanupFunc func(netip.Addr) error) error {
	nodeAllocations := []string{}
	cleanedAllocations := make(map[string]IPAllocation)
	vmCleanupResults := make(map[string]error) // Track cleanup success/failure

	// Separate current node's allocations from others
	for allocID, allocation := range state.AllocatedIPs {
		if allocation.NodeName == nodeName {
			// This IP was allocated on the restarting node - release it
			nodeAllocations = append(nodeAllocations, allocation.IP)
			logger.Printf("Found IP %s to release from node %s (pod %s/%s)",
				allocation.IP, nodeName, allocation.PodNamespace, allocation.PodName)
		} else {
			// Keep allocations from other nodes
			cleanedAllocations[allocID] = allocation
		}
	}

	if len(nodeAllocations) == 0 {
		logger.Printf("No IP allocations found for node %s", nodeName)
		return nil
	}

	// Send reboot files to all VMs FIRST (critical for VM state consistency)
	if vmCleanupFunc != nil {
		logger.Printf("Sending reboot files to %d VMs before releasing IPs", len(nodeAllocations))

		for _, ipStr := range nodeAllocations {
			if ip, err := netip.ParseAddr(ipStr); err == nil {
				cleanupErr := vmCleanupFunc(ip)
				vmCleanupResults[ipStr] = cleanupErr

				if cleanupErr != nil {
					logger.Printf("Warning: failed to send reboot file to VM %s: %v", ip.String(), cleanupErr)
				} else {
					logger.Printf("Successfully sent reboot file to VM %s", ip.String())
				}
			}
		}

		// Wait for VMs to process reboot (allow VM state to settle)
		waitDuration := 15 * time.Second // TODO: Make this configurable via byom config
		logger.Printf("Waiting %v for VMs to process reboot files", waitDuration)

		select {
		case <-time.After(waitDuration):
			// Continue after wait
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during VM cleanup wait: %w", ctx.Err())
		}
	}

	// Only release IPs for VMs that successfully received reboot files
	successfullyCleanedIPs := []string{}
	failedCleanupIPs := []string{}

	for _, ipStr := range nodeAllocations {
		if vmCleanupFunc == nil {
			// If no cleanup function provided, release all IPs (backward compatibility)
			successfullyCleanedIPs = append(successfullyCleanedIPs, ipStr)
		} else if cleanupErr := vmCleanupResults[ipStr]; cleanupErr != nil {
			failedCleanupIPs = append(failedCleanupIPs, ipStr)
			logger.Printf("NOT releasing IP %s due to failed VM cleanup: %v", ipStr, cleanupErr)
		} else {
			successfullyCleanedIPs = append(successfullyCleanedIPs, ipStr)
		}
	}

	// Update state - only release successfully cleaned IPs
	updatedAvailable := append(state.AvailableIPs, successfullyCleanedIPs...)

	// Keep failed cleanup IPs as allocated to prevent reuse
	for allocID, allocation := range state.AllocatedIPs {
		if allocation.NodeName == nodeName {
			found := false
			for _, failedIP := range failedCleanupIPs {
				if allocation.IP == failedIP {
					cleanedAllocations[allocID] = allocation // Keep as allocated
					found = true
					break
				}
			}
			if !found {
				// This IP was successfully cleaned, so it's being released
				logger.Printf("Releasing IP %s from node %s", allocation.IP, nodeName)
			}
		}
	}

	newState := &IPAllocationState{
		AllocatedIPs: cleanedAllocations,
		AvailableIPs: updatedAvailable,
		LastUpdated:  metav1.Now(),
		Version:      state.Version + 1,
	}

	logger.Printf("Released %d IPs, kept %d IPs allocated due to cleanup failures for node %s",
		len(successfullyCleanedIPs), len(failedCleanupIPs), nodeName)

	return cm.updateState(ctx, newState)
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
