// (C) Copyright IBM Corp. 2022.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/podnetwork/tunneler"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/netops"
	"golang.org/x/sys/unix"
)

type podNodeTunneler struct{}

func NewPodNodeTunneler() tunneler.Tunneler {
	return &podNodeTunneler{}
}

const (
	podVethName  = "eth0"
	hostVethName = "veth0"

	podTableID          = 45001
	sourceTableID       = 45002
	sourceTablePriority = 505
)

func checkDefaultRoute(dst *net.IPNet) bool {

	if dst == nil || dst.IP == nil {
		return true
	}

	if !dst.IP.Equal(net.IPv4zero) {
		return false
	}

	if dst.Mask == nil {
		return false
	}

	ones, bits := dst.Mask.Size()
	if bits == 0 {
		return false
	}

	return ones == 0
}

func (t *podNodeTunneler) Setup(nsPath string, podNodeIPs []net.IP, config *tunneler.Config) error {

	if !config.Dedicated {
		return errors.New("shared subnet is not supported")
	}

	if len(podNodeIPs) != 2 {
		return errors.New("secondary pod node IP is not available")
	}

	podNodeIP := podNodeIPs[1]

	podIP, podIPNet, err := net.ParseCIDR(config.PodIP)
	if err != nil {
		return fmt.Errorf("failed to parse pod IP %s: %w", config.PodIP, err)
	}
	podIPNet.IP = podIP

	nodeIP, _, err := net.ParseCIDR(config.WorkerNodeIP)
	if err != nil {
		return fmt.Errorf("failed to parse node IP %s: %w", config.WorkerNodeIP, err)
	}

	hostNS, err := netops.OpenCurrentNamespace()
	if err != nil {
		return fmt.Errorf("failed to get host network namespace: %w", err)
	}
	defer hostNS.Close()

	hostInterface, err := hostNS.LinkNameByAddr(podNodeIP)
	if err != nil {
		return fmt.Errorf("failed to identify host interface that has %s on netns %s", podNodeIP.String(), hostNS.Path())
	}

	podNS, err := netops.OpenNamespace(nsPath)
	if err != nil {
		return fmt.Errorf("failed to get a pod network namespace: %s: %w", nsPath, err)
	}
	defer podNS.Close()

	if err := hostNS.RuleAdd(&netops.Rule{Priority: localTableNewPriority, Table: unix.RT_TABLE_LOCAL}); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("failed to add local table at priority %d: %w", localTableNewPriority, err)
	}

	if err = hostNS.RuleDel(&netops.Rule{Priority: localTableOriginalPriority, Table: unix.RT_TABLE_LOCAL}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete local table at priority %d: %w", localTableOriginalPriority, err)
	}

	if err := hostNS.VethAdd(hostVethName, podNS, podVethName); err != nil {
		return fmt.Errorf("failed to create a veth pair: %s and %s on %s: %w", hostVethName, podVethName, nsPath, err)
	}

	mtu := int(config.MTU)
	if err := podNS.SetMTU(podVethName, mtu); err != nil {
		return fmt.Errorf("failed to set MTU of %s to %d on %s: %w", podVethName, mtu, nsPath, err)
	}

	if err := podNS.AddrAdd(podVethName, podIPNet); err != nil {
		return fmt.Errorf("failed to add pod IP %s to %s on %s: %w", podIPNet, podVethName, nsPath, err)
	}

	if err := podNS.LinkSetUp(podVethName); err != nil {
		return fmt.Errorf("failed to set %s up on %s: %w", podVethName, nsPath, err)
	}

	if err := hostNS.LinkSetUp(hostVethName); err != nil {
		return fmt.Errorf("failed to set %s up on host network namespace: %w", hostVethName, err)
	}

	var defaultRouteGateway net.IP

	// We need to process routes without gateway address first. Processing routes with a gateway causes an error if the gateway is not reachable.
	// Calico sets up routes with this pattern.
	// https://github.com/projectcalico/cni-plugin/blob/7495c0279c34faac315b82c1838bca638e23dbbe/pkg/dataplane/linux/dataplane_linux.go#L158-L167

	var first, second []*tunneler.Route
	for _, route := range config.Routes {
		if route.GW == "" {
			first = append(first, route)
		} else {
			second = append(second, route)
		}
	}
	routes := append(first, second...)

	for _, route := range routes {
		var dst *net.IPNet
		if route.Dst != "" {
			var err error
			_, dst, err = net.ParseCIDR(route.Dst)
			if err != nil {
				return fmt.Errorf("failed to add route destination %s: %w", route.Dst, err)
			}
		}
		var gw net.IP
		if route.GW != "" {
			gw = net.ParseIP(route.GW)
			if gw == nil {
				return fmt.Errorf("failed to parse GW IP: %s", route.GW)
			}
		}

		if err := podNS.RouteAdd(&netops.Route{Destination: dst, Gateway: gw, Device: podVethName}); err != nil {
			return fmt.Errorf("failed to add a route to %s via %s on pod network namespace %s: %w", dst, gw, nsPath, err)
		}

		if checkDefaultRoute(dst) {
			defaultRouteGateway = gw
		}
	}

	if defaultRouteGateway == nil {
		return errors.New("no default route gateway is specified")
	}

	if err := hostNS.AddrAdd(hostVethName, mask32(defaultRouteGateway)); err != nil {
		return fmt.Errorf("failed to add GW IP %s to %s on host network namespace: %w", defaultRouteGateway, hostVethName, err)
	}

	if err := hostNS.RouteAdd(&netops.Route{Destination: mask32(podIP), Device: hostVethName, Table: podTableID}); err != nil {
		return fmt.Errorf("failed to add route table %d to pod %s IP on host network namespace: %w", podTableID, podIP, err)
	}

	if err := hostNS.RouteAdd(&netops.Route{Gateway: nodeIP, Device: hostInterface, Table: sourceTableID}); err != nil {
		return fmt.Errorf("failed to add route table %d to pod %s IP on host network namespace: %w", sourceTableID, podIP, err)
	}

	if err := hostNS.RuleAdd(&netops.Rule{Priority: podTablePriority, Table: podTableID}); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("failed to add route table %d for pod IP at priority %d: %w", podTableID, podTablePriority, err)
	}

	if err := hostNS.RuleAdd(&netops.Rule{Src: mask32(podIP), IifName: hostVethName, Priority: sourceTablePriority, Table: sourceTableID}); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("failed to add route table %d for source routing at priority %d: %w", sourceTableID, sourceTablePriority, err)
	}

	for key, val := range map[string]string{
		"net/ipv4/ip_forward": "1",
		fmt.Sprintf("net/ipv4/conf/%s/proxy_arp", hostVethName):    "1",
		fmt.Sprintf("net/ipv4/neigh/%s/proxy_delay", hostVethName): "0",
	} {
		if err := sysctlSet(hostNS, key, val); err != nil {
			return err
		}
	}

	return nil
}

func (t *podNodeTunneler) Teardown(nsPath, hostInterface string, config *tunneler.Config) error {
	return nil
}
