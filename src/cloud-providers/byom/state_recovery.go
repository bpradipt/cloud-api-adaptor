// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package byom

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
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

		// Release node allocations and repair state in single atomic operation
		if err := cm.releaseNodeAllocationsAndRepairState(ctx, state, currentNode, vmCleanupFunc); err != nil {
			logger.Printf("Warning: failed to release node allocations and repair state: %v", err)
		}

		return nil
	}

	// ConfigMap doesn't exist or is corrupted, initialize empty state
	logger.Printf("ConfigMap state not available, initializing empty state")
	return cm.initializeAndSaveEmptyState(ctx)
}

// releaseNodeAllocationsAndRepairState releases node allocations with VM cleanup and repairs state to match primary config
// Final AvailableIPs = config.PoolIPs - (other nodes' allocated IPs) - (current node IPs that failed cleanup)
func (cm *ConfigMapVMPoolManager) releaseNodeAllocationsAndRepairState(ctx context.Context, state *IPAllocationState, nodeName string, vmCleanupFunc func(context.Context, netip.Addr) error) error {
	allocationsToProcess := make(map[string]IPAllocation)
	finalAllocatedIPs := make(map[string]IPAllocation)

	// 1. Separate current node allocations from other nodes
	for allocID, allocation := range state.AllocatedIPs {
		if allocation.NodeName == nodeName {
			allocationsToProcess[allocation.IP] = allocation
		} else {
			finalAllocatedIPs[allocID] = allocation // Keep allocations from other nodes
		}
	}

	if len(allocationsToProcess) == 0 {
		logger.Printf("No IP allocations found for node %s", nodeName)
	} else {
		logger.Printf("Found %d IPs to process from node %s", len(allocationsToProcess), nodeName)

		// 2. Perform VM cleanup concurrently
		var cleanupResults sync.Map // Store cleanup errors
		if vmCleanupFunc != nil {
			g, gCtx := errgroup.WithContext(ctx)

			for ipStr := range allocationsToProcess {
				ipStr := ipStr // Capture loop variable for the goroutine
				g.Go(func() error {
					ip, err := netip.ParseAddr(ipStr)
					if err != nil {
						return fmt.Errorf("invalid IP in allocation state: %s", ipStr)
					}

					logger.Printf("Sending reboot file to VM %s", ip.String())
					if cleanupErr := vmCleanupFunc(gCtx, ip); cleanupErr != nil {
						logger.Printf("Warning: failed to send reboot file to VM %s: %v", ip.String(), cleanupErr)
						cleanupResults.Store(ipStr, cleanupErr)
					}
					return nil // Don't fail the whole group for one failed cleanup
				})
			}

			// Wait for all cleanup goroutines to finish
			if err := g.Wait(); err != nil {
				return fmt.Errorf("error during concurrent cleanup: %w", err)
			}

			// Wait for VMs to process reboot files
			waitDuration := 15 * time.Second // TODO: Make configurable
			logger.Printf("Waiting %v for VMs to process reboot files", waitDuration)
			select {
			case <-time.After(waitDuration):
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during VM cleanup wait: %w", ctx.Err())
			}
		}

		// 3. Keep current node IPs as allocated if cleanup failed
		successfullyCleanedCount := 0
		for ip, allocation := range allocationsToProcess {
			if _, failed := cleanupResults.Load(ip); failed {
				// Cleanup failed, keep IP as allocated to prevent reuse of dirty VM
				for id, alloc := range state.AllocatedIPs {
					if alloc.IP == ip {
						finalAllocatedIPs[id] = allocation
						break
					}
				}
				logger.Printf("Keeping IP %s allocated for node %s due to failed cleanup", ip, nodeName)
			} else {
				// Cleanup succeeded, IP will become available
				successfullyCleanedCount++
				logger.Printf("Successfully cleaned IP %s from node %s", ip, nodeName)
			}
		}

		logger.Printf("Released %d IPs, kept %d IPs allocated due to cleanup failures for node %s",
			successfullyCleanedCount, len(allocationsToProcess)-successfullyCleanedCount, nodeName)
	}

	// 4. Repair state to match primary configuration: AvailableIPs = config.PoolIPs - finalAllocatedIPs
	primaryIPSet := make(map[string]bool)
	for _, ip := range cm.config.PoolIPs {
		primaryIPSet[ip] = true
	}

	// Filter out any allocated IPs not in primary configuration
	validAllocatedIPs := make(map[string]IPAllocation)
	allocatedIPSet := make(map[string]bool)
	for allocID, allocation := range finalAllocatedIPs {
		if primaryIPSet[allocation.IP] {
			validAllocatedIPs[allocID] = allocation
			allocatedIPSet[allocation.IP] = true
		} else {
			logger.Printf("Warning: removing allocated IP %s not in primary configuration", allocation.IP)
		}
	}

	// Build AvailableIPs = primary config - allocated IPs
	availableIPs := []string{}
	for _, ip := range cm.config.PoolIPs {
		if !allocatedIPSet[ip] {
			availableIPs = append(availableIPs, ip)
		}
	}

	// 5. Single atomic state update
	finalState := &IPAllocationState{
		AllocatedIPs: validAllocatedIPs,
		AvailableIPs: availableIPs,
		LastUpdated:  metav1.Now(),
		Version:      state.Version + 1,
	}

	logger.Printf("Final state: primary config has %d IPs, %d allocated, %d available",
		len(cm.config.PoolIPs), len(validAllocatedIPs), len(availableIPs))

	if err := cm.updateState(ctx, finalState); err != nil {
		return fmt.Errorf("failed to update final state: %w", err)
	}

	logger.Printf("Successfully released node allocations and repaired state to match primary configuration")
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
