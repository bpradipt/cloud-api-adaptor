package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/proxy"
	daemon "github.com/confidential-containers/cloud-api-adaptor/pkg/forwarder"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/podnetwork"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/podnetwork/tunneler"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/cloudinit"
	"github.com/containerd/containerd/pkg/cri/annotations"

	pb "github.com/kata-containers/kata-containers/src/runtime/protocols/hypervisor"
)

const (
	Version         = "0.0.0"
	DefaultUserName = "ubuntu"
)

type hypervisorService struct {
	azureClient   azcore.TokenCredential
	serviceConfig *Config
	sandboxes     map[sandboxID]*sandbox
	podsDir       string
	daemonPort    string
	nodeName      string
	workerNode    podnetwork.WorkerNode
	sync.Mutex
}

func newService(azureClient azcore.TokenCredential, config *Config, workerNode podnetwork.WorkerNode, podsDir, daemonPort string) pb.HypervisorService {
	logger.Printf("service config %v", config)

	hostname, err := os.Hostname()
	if err != nil {
		panic(fmt.Errorf("failed to get hostname: %w", err))
	}

	i := strings.Index(hostname, ".")
	if i >= 0 {
		hostname = hostname[0:i]
	}

	return &hypervisorService{
		azureClient:   azureClient,
		serviceConfig: config,
		sandboxes:     map[sandboxID]*sandbox{},
		podsDir:       podsDir,
		daemonPort:    daemonPort,
		nodeName:      hostname,
		workerNode:    workerNode,
	}
}

type sandboxID string

type sandbox struct {
	id               sandboxID
	pod              string
	namespace        string
	netNSPath        string
	podDirPath       string
	vsi              string
	agentProxy       proxy.AgentProxy
	podNetworkConfig *tunneler.Config
}

func (s *hypervisorService) Version(ctx context.Context, req *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{Version: Version}, nil
}

func (s *hypervisorService) CreateVM(ctx context.Context, req *pb.CreateVMRequest) (*pb.CreateVMResponse, error) {

	sid := sandboxID(req.Id)

	if sid == "" {
		return nil, errors.New("empty sandbox id")
	}
	s.Lock()
	defer s.Unlock()
	if _, exists := s.sandboxes[sid]; exists {
		return nil, fmt.Errorf("sandbox %s already exists", sid)
	}
	pod := req.Annotations[annotations.SandboxName]
	if pod == "" {
		return nil, fmt.Errorf("pod name %s is missing in annotations", annotations.SandboxName)
	}
	namespace := req.Annotations[annotations.SandboxNamespace]
	if namespace == "" {
		return nil, fmt.Errorf("namespace name %s is missing in annotations", annotations.SandboxNamespace)
	}

	podDirPath := filepath.Join(s.podsDir, string(sid))
	if err := os.MkdirAll(podDirPath, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create a pod directory: %s: %w", podDirPath, err)
	}

	socketPath := filepath.Join(podDirPath, proxy.SocketName)

	netNSPath := req.NetworkNamespacePath

	podNetworkConfig, err := s.workerNode.Inspect(netNSPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect netns %s: %w", netNSPath, err)
	}

	agentProxy := proxy.NewAgentProxy(socketPath)

	sandbox := &sandbox{
		id:               sid,
		pod:              pod,
		namespace:        namespace,
		netNSPath:        netNSPath,
		podDirPath:       podDirPath,
		agentProxy:       agentProxy,
		podNetworkConfig: podNetworkConfig,
	}
	s.sandboxes[sid] = sandbox
	logger.Printf("create a sandbox %s for pod %s in namespace %s (netns: %s)", req.Id, pod, namespace, sandbox.netNSPath)
	return &pb.CreateVMResponse{AgentSocketPath: socketPath}, nil
}

func (s *hypervisorService) StartVM(ctx context.Context, req *pb.StartVMRequest) (*pb.StartVMResponse, error) {

	sandbox, err := s.getSandbox(req.Id)
	if err != nil {
		return nil, err
	}

	daemonConfig := daemon.Config{
		PodNamespace: sandbox.namespace,
		PodName:      sandbox.pod,
		PodNetwork:   sandbox.podNetworkConfig,
	}
	daemonJSON, err := json.MarshalIndent(daemonConfig, "", "    ")
	if err != nil {
		return nil, err
	}

	// Store daemon.json in worker node for debugging
	if err = os.WriteFile(filepath.Join(sandbox.podDirPath, "daemon.json"), daemonJSON, 0666); err != nil {
		return nil, fmt.Errorf("failed to store daemon.json at %s: %w", sandbox.podDirPath, err)
	}
	logger.Printf("store daemon.json at %s", sandbox.podDirPath)

	cloudConfig := &cloudinit.CloudConfig{
		WriteFiles: []cloudinit.WriteFile{
			{
				Path:    daemon.DefaultConfigPath,
				Content: string(daemonJSON),
			},
		},
	}

	userData, err := cloudConfig.Generate()
	if err != nil {
		return nil, err
	}

	//Convert userData to base64
	userDataEnc := base64.StdEncoding.EncodeToString([]byte(userData))

	vmName := fmt.Sprintf("%s-%s-%s-%.8s", s.nodeName, sandbox.namespace, sandbox.pod, sandbox.id)
	diskName := fmt.Sprintf("%s-disk", vmName)
	nicName := fmt.Sprintf("%s-net", vmName)

	// Get NIC using subnet and allow ports on the ssh group
	err = s.createNetworkInterface(ctx, nicName)
	if err != nil {
		return nil, err
	}

	vmParameters := armcompute.VirtualMachine{
		Location: to.Ptr(s.serviceConfig.Region),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(s.serviceConfig.Size)),
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					CommunityGalleryImageID: to.Ptr(s.serviceConfig.ImageId),
				},
				OSDisk: &armcompute.OSDisk{
					Name:         to.Ptr(diskName),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
					ManagedDisk: &armcompute.ManagedDiskParameters{
						StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS),
					},
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName: to.Ptr(vmName),
				CustomData:   to.Ptr(userDataEnc),
				LinuxConfiguration: &armcompute.LinuxConfiguration{
					DisablePasswordAuthentication: to.Ptr(true),
					//TBD: replace with a suitable mechanism to use precreated SSH key
					SSH: &armcompute.SSHConfiguration{
						PublicKeys: []*armcompute.SSHPublicKey{{
							Path:    to.Ptr(fmt.Sprintf("/home/%s/.ssh/authorized_keys", DefaultUserName)),
							KeyData: to.Ptr("aaaaaa"),
						}},
					},
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaceConfigurations: []*armcompute.VirtualMachineNetworkInterfaceConfiguration{
					{
						Name: to.Ptr(nicName),
						Properties: &armcompute.VirtualMachineNetworkInterfaceConfigurationProperties{
							IPConfigurations: []*armcompute.VirtualMachineNetworkInterfaceIPConfiguration{
								{
									Name: to.Ptr(nicName + "-conf"),
									Properties: &armcompute.VirtualMachineNetworkInterfaceIPConfigurationProperties{
										Primary: to.Ptr(true),
										Subnet: &armcompute.SubResource{
											ID: to.Ptr(s.serviceConfig.SubnetId),
										},
									},
								},
							},
							Primary:      to.Ptr(true),
							DeleteOption: to.Ptr(armcompute.DeleteOptionsDelete),
							NetworkSecurityGroup: &armcompute.SubResource{
								ID: to.Ptr(s.serviceConfig.SecurityGroupId),
							},
						},
					},
				},
			},
		},
	}

	err = CreateInstance(context.TODO(), s.azureClient, &vmParameters)
	if err != nil {
		return nil, fmt.Errorf("Creating instance returned error: %s", err)
	}

	//Set vsi to instance id
	sandbox.vsi = *vmParameters.ID

	logger.Printf("created an instance %s for sandbox %s", *vmParameters.Name, req.Id)

	podNodeIPs, err := getIPs(vmParameters)
	if err != nil {
		logger.Printf("failed to get IPs for the instance : %v ", err)
		return nil, err
	}

	if err := s.workerNode.Setup(sandbox.netNSPath, podNodeIPs, sandbox.podNetworkConfig); err != nil {
		return nil, fmt.Errorf("failed to set up pod network tunnel on netns %s: %w", sandbox.netNSPath, err)
	}

	serverURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(podNodeIPs[0].String(), s.daemonPort),
		Path:   daemon.AgentURLPath,
	}

	errCh := make(chan error)
	go func() {
		defer close(errCh)

		if err := sandbox.agentProxy.Start(context.Background(), serverURL); err != nil {
			logger.Printf("error running agent proxy: %v", err)
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case <-sandbox.agentProxy.Ready():
	}

	logger.Printf("agent proxy is ready")
	return &pb.StartVMResponse{}, nil
}

func (s *hypervisorService) getSandbox(id string) (*sandbox, error) {

	sid := sandboxID(id)

	if id == "" {
		return nil, errors.New("empty sandbox id")
	}
	s.Lock()
	defer s.Unlock()
	if _, exists := s.sandboxes[sid]; !exists {
		return nil, fmt.Errorf("sandbox %s does not exist", sid)
	}
	return s.sandboxes[sid], nil
}

var errNotReady = errors.New("address not ready")

func getIPs(vm armcompute.VirtualMachine) ([]net.IP, error) {
	var podNodeIPs []net.IP

	//Get VM IP

	return podNodeIPs, nil
}

func (s *hypervisorService) deleteInstance(id string) error {

	err := DeleteInstance(context.TODO(), s.azureClient, id)

	if err != nil {
		logger.Printf("failed to delete an instance: %v", err)
		return err
	}
	logger.Printf("deleted an instance %s", id)
	return nil
}

func (s *hypervisorService) StopVM(ctx context.Context, req *pb.StopVMRequest) (*pb.StopVMResponse, error) {
	sandbox, err := s.getSandbox(req.Id)
	if err != nil {
		return nil, err
	}

	if err := sandbox.agentProxy.Shutdown(); err != nil {
		logger.Printf("failed to stop agent proxy: %v", err)
	}

	if err := s.deleteInstance(sandbox.vsi); err != nil {
		return nil, err
	}

	if err := s.workerNode.Teardown(sandbox.netNSPath, sandbox.podNetworkConfig); err != nil {
		return nil, fmt.Errorf("failed to tear down netns %s: %w", sandbox.netNSPath, err)
	}

	return &pb.StopVMResponse{}, nil
}

func (s *hypervisorService) createNetworkInterface(ctx context.Context, nicName string) error {
	return nil
}
