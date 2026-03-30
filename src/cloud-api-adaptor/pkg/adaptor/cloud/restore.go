// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-api-adaptor/pkg/adaptor/proxy"
	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-api-adaptor/pkg/forwarder"
	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-api-adaptor/pkg/podnetwork/tunneler"
	putil "github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers/util"
)


const (
	stateFileName      = "state.json"
	restoreDialTimeout = 30 * time.Second
)

// sandboxState holds the information needed to reconstruct a sandbox after a CAA restart.
type sandboxState struct {
	SandboxID    string           `json:"sandbox_id"`
	InstanceID   string           `json:"instance_id"`
	InstanceName string           `json:"instance_name"`
	InstanceIPs  []netip.Addr     `json:"instance_ips"`
	PodName      string           `json:"pod_name"`
	PodNamespace string           `json:"pod_namespace"`
	NetNSPath    string           `json:"net_ns_path"`
	PodNetwork   *tunneler.Config `json:"pod_network"`
}

func sandboxStateFile(podsDir, sid string) string {
	return filepath.Join(podsDir, sid, stateFileName)
}

func writeSandboxState(podsDir string, state *sandboxState) error {
	data, err := json.MarshalIndent(state, "", "    ")
	if err != nil {
		return fmt.Errorf("marshaling sandbox state: %w", err)
	}
	path := sandboxStateFile(podsDir, state.SandboxID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing sandbox state to %s: %w", path, err)
	}
	return nil
}

func deleteSandboxState(podsDir, sid string) {
	path := sandboxStateFile(podsDir, sid)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logger.Printf("failed to delete sandbox state %s: %v", path, err)
	}
}

// Restore scans the pods directory for persisted sandbox state and recreates
// agent proxies for any sandboxes whose VMs are still reachable. This must be
// called before the ttrpc server starts accepting connections.
func (s *cloudService) Restore(ctx context.Context) error {
	entries, err := os.ReadDir(s.serverConfig.PodsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading pods dir %s: %w", s.serverConfig.PodsDir, err)
	}

	var wg sync.WaitGroup
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sid := entry.Name()
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if err := s.restoreSandbox(ctx, sid); err != nil {
				logger.Printf("failed to restore sandbox %s: %v (skipping)", sid, err)
			}
		}(sid)
	}
	wg.Wait()
	return nil
}

func (s *cloudService) restoreSandbox(ctx context.Context, sid string) error {
	statePath := sandboxStateFile(s.serverConfig.PodsDir, sid)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no state file, sandbox was not fully started
		}
		return fmt.Errorf("reading state file: %w", err)
	}

	var state sandboxState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parsing state file: %w", err)
	}

	if len(state.InstanceIPs) == 0 {
		return fmt.Errorf("no instance IPs in state")
	}

	logger.Printf("restoring sandbox %s (instance: %s, IPs: %v)", sid, state.InstanceID, state.InstanceIPs)

	// workerNode.Setup is intentionally skipped during restore. Kernel network
	// state (VXLAN device, iptables rules, tc filters) is managed by the Linux
	// kernel and persists across CAA process restarts. The pod VM's tunnel end
	// is still live. Re-calling Setup would create a duplicate VXLAN device with
	// the same VXLAN ID against the existing tunnel, breaking pod network
	// communication. netNSPath and podNetwork are still persisted in state.json
	// because workerNode.Teardown needs them when StopVM is eventually called.

	serverURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(state.InstanceIPs[0].String(), s.serverConfig.ForwarderPort),
		Path:   forwarder.AgentURLPath,
	}
	socketPath := filepath.Join(s.serverConfig.PodsDir, sid, proxy.SocketName)
	serverName := putil.GenerateInstanceName(state.PodName, sid, 63)

	agentProxy := s.proxyFactory.New(serverName, socketPath)

	// proxyCtx is derived from the server's long-lived context. Passing it to
	// Start ensures that if the restore times out and we cancel proxyCtx, the
	// cancellation propagates into dial's retry loop (via retry.Context), causing
	// the goroutine to exit cleanly. On success, proxyCtx stays alive so the
	// proxy keeps running until server shutdown or agentProxy.Shutdown() in StopVM.
	proxyCtx, proxyCancel := context.WithCancel(ctx)

	restoreTimer := time.NewTimer(restoreDialTimeout)
	defer restoreTimer.Stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agentProxy.Start(proxyCtx, serverURL)
	}()

	select {
	case <-restoreTimer.C:
		proxyCancel()
		return fmt.Errorf("timed out waiting for VM to become reachable for sandbox %s", sid)
	case err := <-errCh:
		proxyCancel()
		if err != nil {
			return fmt.Errorf("agent proxy failed during restore: %w", err)
		}
		return fmt.Errorf("agent proxy exited unexpectedly during restore of sandbox %s", sid)
	case <-agentProxy.Ready():
		// proxyCtx intentionally not cancelled here — proxy must keep running.
		// It will be cleaned up when the server context is cancelled (shutdown)
		// or when agentProxy.Shutdown() is called from StopVM.
		_ = proxyCancel // suppress "cancel not called" linter warning
	}

	sb := &sandbox{
		id:           sandboxID(sid),
		podName:      state.PodName,
		podNamespace: state.PodNamespace,
		netNSPath:    state.NetNSPath,
		instanceID:   state.InstanceID,
		instanceName: state.InstanceName,
		agentProxy:   agentProxy,
		podNetwork:   state.PodNetwork,
		restored:     true,
	}

	// Note: cloudConfig and spec are intentionally not restored. They are only
	// used in StartVM's provider.CreateInstance call, which is skipped for
	// restored sandboxes via the sandbox.restored guard. If future code ever
	// accesses these fields on a restored sandbox, it must either populate them
	// here from persisted state or add a defensive nil/zero check.
	if err := s.addSandbox(sandboxID(sid), sb); err != nil {
		_ = agentProxy.Shutdown()
		return fmt.Errorf("adding restored sandbox to map: %w", err)
	}

	logger.Printf("successfully restored sandbox %s", sid)
	return nil
}
