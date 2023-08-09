// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"
	"github.com/avast/retry-go/v4"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/adaptor/cloud"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/cloudinit"
)

var logger = log.New(log.Writer(), "[adaptor/cloud/azure] ", log.LstdFlags|log.Lmsgprefix)
var errNotReady = errors.New("address not ready")
var errNotFound = errors.New("VM name not found")

const (
	maxInstanceNameLen = 63
)

type azureProvider struct {
	azureClient   azcore.TokenCredential
	serviceConfig *Config
}

func NewProvider(config *Config) (cloud.Provider, error) {

	logger.Printf("azure config %+v", config.Redact())

	azureClient, err := NewAzureClient(*config)
	if err != nil {
		logger.Printf("creating azure client: %v", err)
		return nil, err
	}

	provider := &azureProvider{
		azureClient:   azureClient,
		serviceConfig: config,
	}

	if err = provider.updateInstanceSizeSpecList(); err != nil {
		return nil, err
	}

	// Initialise VM pool
	// Precreate instances
	if config.PoolSize > 0 {
		if err := provider.initializePodVmPool(context.Background(), config.PoolSize); err != nil {
			return nil, err
		}

		// Start a goroutine to periodically check the pool size
		go provider.checkPodVmPoolSize(context.Background(), config.PoolSize)
	}

	return provider, nil
}

func getIPs(nic *armnetwork.Interface) ([]netip.Addr, error) {
	var podNodeIPs []netip.Addr

	for i, ipc := range nic.Properties.IPConfigurations {
		addr := ipc.Properties.PrivateIPAddress

		if addr == nil || *addr == "" || *addr == "0.0.0.0" {
			return nil, errNotReady
		}

		ip, err := netip.ParseAddr(*addr)
		if err != nil {
			return nil, fmt.Errorf("parsing pod node IP %q: %w", *addr, err)
		}

		podNodeIPs = append(podNodeIPs, ip)
		logger.Printf("podNodeIP[%d]=%s", i, ip.String())
	}

	return podNodeIPs, nil
}

func (p *azureProvider) create(ctx context.Context, parameters *armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return nil, fmt.Errorf("creating VM client: %w", err)
	}

	vmName := *parameters.Properties.OSProfile.ComputerName

	pollerResponse, err := vmClient.BeginCreateOrUpdate(ctx, p.serviceConfig.ResourceGroupName, vmName, *parameters, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning VM creation or update: %w", err)
	}

	resp, err := pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("waiting for the VM creation: %w", err)
	}

	logger.Printf("created VM successfully: %s", *resp.ID)

	return &resp.VirtualMachine, nil
}

func (p *azureProvider) createNetworkInterface(ctx context.Context, nicName string) (*armnetwork.Interface, error) {
	nicClient, err := armnetwork.NewInterfacesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return nil, fmt.Errorf("creating network interfaces client: %w", err)
	}

	parameters := armnetwork.Interface{
		Location: to.Ptr(p.serviceConfig.Region),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{
				{
					Name: to.Ptr(fmt.Sprintf("%s-ipConfig", nicName)),
					Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
						PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
						Subnet: &armnetwork.Subnet{
							ID: to.Ptr(p.serviceConfig.SubnetId),
						},
					},
				},
			},
		},
	}

	if p.serviceConfig.SecurityGroupId != "" {
		parameters.Properties.NetworkSecurityGroup = &armnetwork.SecurityGroup{
			ID: to.Ptr(p.serviceConfig.SecurityGroupId),
		}
	}

	pollerResponse, err := nicClient.BeginCreateOrUpdate(ctx, p.serviceConfig.ResourceGroupName, nicName, parameters, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning creation or update of network interface: %w", err)
	}

	resp, err := pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("polling network interface creation: %w", err)
	}

	return &resp.Interface, nil
}

func (p *azureProvider) CreateInstance(ctx context.Context, podName, sandboxID string, cloudConfig cloudinit.CloudConfigGenerator, spec cloud.InstanceTypeSpec) (*cloud.Instance, error) {

	// cloud.Instance var
	var instance cloud.Instance

	instanceName := util.GenerateInstanceName(podName, sandboxID, maxInstanceNameLen)

	userData, err := cloudConfig.Generate()
	if err != nil {
		return nil, err
	}

	//Convert userData to base64
	userDataEnc := base64.StdEncoding.EncodeToString([]byte(userData))

	// If precreated VMs are available, use one of them
	if len(p.serviceConfig.PreCreatedInstances) > 0 {
		// Get the first pre-created instance
		instance = p.serviceConfig.PreCreatedInstances[0]
		// Remove the first pre-created instance from the list
		p.serviceConfig.PreCreatedInstances = p.serviceConfig.PreCreatedInstances[1:]

		logger.Printf("Using instance(%s) from precreated pool for %s", instance.ID, instance.Name)

		// Modify the instance to set userData
		err := p.modifyInstanceUserData(ctx, instance.Name, userDataEnc)
		if err != nil {
			return nil, err
		}

		// Start the instance
		//err = p.start(ctx, instance.Name)
		//if err != nil {
		//	return nil, err
		//}
		// Log the instance struct
		logger.Printf("Instance details from the pool: %#v", instance)
	} else {
		instanceSize, err := p.selectInstanceType(ctx, spec)
		if err != nil {
			return nil, err
		}

		diskName := fmt.Sprintf("%s-disk", instanceName)
		nicName := fmt.Sprintf("%s-net", instanceName)

		// require ssh key for authentication on linux
		sshPublicKeyPath := os.ExpandEnv(p.serviceConfig.SSHKeyPath)
		var sshBytes []byte
		if _, err := os.Stat(sshPublicKeyPath); err == nil {
			sshBytes, err = os.ReadFile(sshPublicKeyPath)
			if err != nil {
				err = fmt.Errorf("reading ssh public key file: %w", err)
				logger.Printf("%v", err)
				return nil, err
			}
		} else {
			err = fmt.Errorf("ssh public key: %w", err)
			logger.Printf("%v", err)
			return nil, err
		}

		// Get NIC using subnet and allow ports on the ssh group
		vmNIC, err := p.createNetworkInterface(ctx, nicName)
		if err != nil {
			err = fmt.Errorf("creating VM network interface: %w", err)
			logger.Printf("%v", err)
			return nil, err
		}

		vmParameters, err := p.getVMParameters(instanceSize, diskName, userDataEnc, sshBytes, instanceName, vmNIC)
		if err != nil {
			return nil, err
		}

		logger.Printf("CreateInstance: name: %q", instanceName)

		result, err := p.create(ctx, vmParameters)
		if err != nil {
			if err := p.deleteDisk(ctx, diskName); err != nil {
				logger.Printf("deleting disk (%s): %s", diskName, err)
			}
			if err := p.deleteNetworkInterfaceAsync(context.Background(), nicName); err != nil {
				logger.Printf("deleting nic async (%s): %s", nicName, err)
			}
			return nil, fmt.Errorf("Creating instance (%v): %s", result, err)
		}

		instanceID := *result.ID

		ips, err := getIPs(vmNIC)
		if err != nil {
			logger.Printf("getting IPs for the instance : %v ", err)
			return nil, err
		}

		instance.ID = instanceID
		instance.Name = instanceName
		instance.IPs = ips
	}

	return &instance, nil
}

func (p *azureProvider) getVMParameters(instanceSize, diskName, userDataEnc string, sshBytes []byte, instanceName string, vmNIC *armnetwork.Interface) (*armcompute.VirtualMachine, error) {
	var managedDiskParams *armcompute.ManagedDiskParameters
	var securityProfile *armcompute.SecurityProfile
	if !p.serviceConfig.DisableCVM {
		managedDiskParams = &armcompute.ManagedDiskParameters{
			StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS),
			SecurityProfile: &armcompute.VMDiskSecurityProfile{
				SecurityEncryptionType: to.Ptr(armcompute.SecurityEncryptionTypesVMGuestStateOnly),
			},
		}

		securityProfile = &armcompute.SecurityProfile{
			SecurityType: to.Ptr(armcompute.SecurityTypesConfidentialVM),
			UefiSettings: &armcompute.UefiSettings{
				SecureBootEnabled: to.Ptr(true),
				VTpmEnabled:       to.Ptr(true),
			},
		}
	} else {
		managedDiskParams = &armcompute.ManagedDiskParameters{
			StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS),
		}

		securityProfile = nil
	}

	imgRef := &armcompute.ImageReference{
		ID: to.Ptr(p.serviceConfig.ImageId),
	}
	if strings.HasPrefix(p.serviceConfig.ImageId, "/CommunityGalleries/") {
		imgRef = &armcompute.ImageReference{
			CommunityGalleryImageID: to.Ptr(p.serviceConfig.ImageId),
		}
	}

	// Add tags to the instance
	tags := map[string]*string{}

	// Add custom tags from serviceConfig.Tags to the instance
	for k, v := range p.serviceConfig.Tags {
		tags[k] = to.Ptr(v)
	}

	vmParameters := armcompute.VirtualMachine{
		Location: to.Ptr(p.serviceConfig.Region),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(instanceSize)),
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: imgRef,
				OSDisk: &armcompute.OSDisk{
					Name:         to.Ptr(diskName),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
					DeleteOption: to.Ptr(armcompute.DiskDeleteOptionTypesDelete),
					ManagedDisk:  managedDiskParams,
				},
			},
			OSProfile: &armcompute.OSProfile{
				AdminUsername: to.Ptr(p.serviceConfig.SSHUserName),
				ComputerName:  to.Ptr(instanceName),
				CustomData:    to.Ptr(userDataEnc),
				LinuxConfiguration: &armcompute.LinuxConfiguration{
					DisablePasswordAuthentication: to.Ptr(true),
					//TBD: replace with a suitable mechanism to use precreated SSH key
					SSH: &armcompute.SSHConfiguration{
						PublicKeys: []*armcompute.SSHPublicKey{{
							Path:    to.Ptr(fmt.Sprintf("/home/%s/.ssh/authorized_keys", p.serviceConfig.SSHUserName)),
							KeyData: to.Ptr(string(sshBytes)),
						}},
					},
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{
					{
						ID: vmNIC.ID,
						Properties: &armcompute.NetworkInterfaceReferenceProperties{
							DeleteOption: to.Ptr(armcompute.DeleteOptionsDelete),
						},
					},
				},
			},
			SecurityProfile: securityProfile,
		},
		// Add tags to the instance
		Tags: tags,
	}

	return &vmParameters, nil
}

func (p *azureProvider) DeleteInstance(ctx context.Context, instanceID string) error {
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating VM client: %w", err)
	}

	// instanceID in the form of /subscriptions/<subID>/resourceGroups/<resource_name>/providers/Microsoft.Compute/virtualMachines/<VM_Name>.
	re := regexp.MustCompile(`^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.Compute/virtualMachines/(.*)$`)
	match := re.FindStringSubmatch(instanceID)
	if len(match) < 1 {
		logger.Print("finding VM name using regexp:", match)
		return errNotFound
	}

	vmName := match[1]

	pollerResponse, err := vmClient.BeginDelete(ctx, p.serviceConfig.ResourceGroupName, vmName, nil)
	if err != nil {
		return fmt.Errorf("beginning VM deletion: %w", err)
	}

	if _, err = pollerResponse.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("waiting for the VM deletion: %w", err)
	}

	logger.Printf("deleted VM successfully: %s", vmName)
	return nil
}

func (p *azureProvider) deleteDisk(ctx context.Context, diskName string) error {
	diskClient, err := armcompute.NewDisksClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating disk client: %w", err)
	}

	pollerResponse, err := diskClient.BeginDelete(ctx, p.serviceConfig.ResourceGroupName, diskName, nil)
	if err != nil {
		return fmt.Errorf("beginning disk deletion: %w", err)
	}

	_, err = pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("waiting for the disk deletion: %w", err)
	}

	logger.Printf("deleted disk successfully: %s", diskName)

	return nil
}

func (p *azureProvider) deleteNetworkInterfaceAsync(ctx context.Context, nicName string) error {
	nicClient, err := armnetwork.NewInterfacesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating network interface client: %w", err)
	}
	rg := p.serviceConfig.ResourceGroupName

	// retry with exponential backoff
	go func() {
		err := retry.Do(func() error {
			pollerResponse, err := nicClient.BeginDelete(ctx, rg, nicName, nil)
			if err != nil {
				return fmt.Errorf("beginning network interface deletion: %w", err)
			}
			_, err = pollerResponse.PollUntilDone(ctx, nil)
			if err != nil {
				return fmt.Errorf("waiting for network interface deletion: %w", err)
			}
			return nil
		},
			retry.Context(ctx),
			retry.Attempts(4),
			retry.Delay(180*time.Second),
			retry.MaxDelay(180*time.Second),
			retry.LastErrorOnly(true),
		)
		if err != nil {
			logger.Printf("deleting network interface in background (%s): %s", nicName, err)
		} else {
			logger.Printf("successfully deleted nic (%s) in background", nicName)
		}
	}()

	return nil
}

func (p *azureProvider) Teardown() error {
	// If podVM pool exists delete it
	if p.serviceConfig.PoolSize > 0 {
		err := p.destroyPodVmPool(context.Background())
		if err != nil {
			return fmt.Errorf("destroying podVM pool: %w", err)
		}
	}

	return nil
}

// Add SelectInstanceType method to select an instance type based on the memory and vcpu requirements
func (p *azureProvider) selectInstanceType(ctx context.Context, spec cloud.InstanceTypeSpec) (string, error) {

	return cloud.SelectInstanceTypeToUse(spec, p.serviceConfig.InstanceSizeSpecList, p.serviceConfig.InstanceSizes, p.serviceConfig.Size)
}

// Add a method to populate InstanceSizeSpecList for all the instanceSizes
// available in Azure
func (p *azureProvider) updateInstanceSizeSpecList() error {

	// Create a new instance of the Virtual Machine Sizes client
	vmSizesClient, err := armcompute.NewVirtualMachineSizesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating VM sizes client: %w", err)
	}
	// Get the instance sizes from the service config
	instanceSizes := p.serviceConfig.InstanceSizes

	// If instanceTypes is empty then populate it with the default instance type
	if len(instanceSizes) == 0 {
		instanceSizes = append(instanceSizes, p.serviceConfig.Size)
	}

	// Create a list of instancesizespec
	var instanceSizeSpecList []cloud.InstanceTypeSpec

	// TODO: Is there an optimal method for this?
	// Create NewListPager to iterate over the instance types
	pager := vmSizesClient.NewListPager(p.serviceConfig.Region, &armcompute.VirtualMachineSizesClientListOptions{})

	// Iterate over the page and populate the instanceSizeSpecList for all the instanceSizes
	for pager.More() {
		nextResult, err := pager.NextPage(context.Background())
		if err != nil {
			return fmt.Errorf("getting next page of VM sizes: %w", err)
		}
		for _, vmSize := range nextResult.VirtualMachineSizeListResult.Value {
			if util.Contains(instanceSizes, *vmSize.Name) {
				instanceSizeSpecList = append(instanceSizeSpecList, cloud.InstanceTypeSpec{InstanceType: *vmSize.Name, VCPUs: int64(*vmSize.NumberOfCores), Memory: int64(*vmSize.MemoryInMB)})
			}
		}
	}

	// Sort the InstanceSizeSpecList by Memory and update the serviceConfig
	p.serviceConfig.InstanceSizeSpecList = cloud.SortInstanceTypesOnMemory(instanceSizeSpecList)
	logger.Printf("instanceSizeSpecList (%v)", p.serviceConfig.InstanceSizeSpecList)
	return nil
}

// Add a method to precreate some instances in stopped state using
// Take the number of instances to be created as an argument
// Take the vmParameters parameters from serviceConfig
// Return the cloud.Instance slice
func (p *azureProvider) initializePodVmPool(ctx context.Context, numInstances int) error {

	// Create a slice of cloud.Instance
	instances := make([]cloud.Instance, numInstances)

	// Precreate numInstances instances in stopped state
	// Precreated instances are of one type and one image
	// Precreated instances cannot be customized using pod annotations
	for i := 0; i < numInstances; i++ {

		// Generate a random string to be used as the sandboxID for the precreated instances
		sandboxID := util.GenerateRandomString(8)
		podName := "ready"
		instanceName := util.GenerateInstanceName(podName, sandboxID, maxInstanceNameLen)

		diskName := fmt.Sprintf("%s-disk", instanceName)
		nicName := fmt.Sprintf("%s-net", instanceName)

		// require ssh key for authentication on linux
		sshPublicKeyPath := os.ExpandEnv(p.serviceConfig.SSHKeyPath)
		var sshBytes []byte
		if _, err := os.Stat(sshPublicKeyPath); err == nil {
			sshBytes, err = os.ReadFile(sshPublicKeyPath)
			if err != nil {
				err = fmt.Errorf("reading ssh public key file: %w", err)
				logger.Printf("%v", err)
				return err
			}
		} else {
			err = fmt.Errorf("ssh public key: %w", err)
			logger.Printf("%v", err)
			return err
		}

		// Get NIC using subnet and allow ports on the ssh group
		vmNIC, err := p.createNetworkInterface(ctx, nicName)
		if err != nil {
			err = fmt.Errorf("creating VM network interface: %w", err)
			logger.Printf("%v", err)
			return err
		}

		vmParameters, err := p.getVMParameters(p.serviceConfig.Size, diskName, "", sshBytes, instanceName, vmNIC)
		if err != nil {
			return err
		}

		// Create the VM
		result, err := p.create(ctx, vmParameters)
		if err != nil {
			if err := p.deleteDisk(ctx, diskName); err != nil {
				logger.Printf("deleting disk (%s): %s", diskName, err)
			}
			if err := p.deleteNetworkInterfaceAsync(context.Background(), nicName); err != nil {
				logger.Printf("deleting nic async (%s): %s", nicName, err)
			}
			return fmt.Errorf("pre-creating instance (%v): %s", result, err)
		}

		instanceID := *result.ID

		ips, err := getIPs(vmNIC)
		if err != nil {
			logger.Printf("getting IPs for the instance : %v ", err)
			return err
		}

		instance := cloud.Instance{
			ID:   instanceID,
			Name: instanceName,
			IPs:  ips,
		}
		instances[i] = instance

		// Stop the instance
		//if err := p.stop(ctx, instanceName); err != nil {
		//	logger.Printf("stopping instance (%s): %s", instanceID, err)
		//	return err
		//}

	}

	// Update config.PreCreatedInstances with the instances
	// If config.PreCreatedInstances is empty then add the instances var to it
	// If config.PreCreatedInstances is not empty then append the instances var to it
	p.serviceConfig.PreCreatedInstances = append(p.serviceConfig.PreCreatedInstances, instances...)

	logger.Printf("PreCreatedInstances (%v)", p.serviceConfig.PreCreatedInstances)

	return nil

}

// Stop the instance
func (p *azureProvider) stop(ctx context.Context, instanceName string) error {

	// Create a new instance of the Virtual Machines client
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating VM client: %w", err)
	}

	// Stop the instance
	pollerResponse, err := vmClient.BeginPowerOff(ctx, p.serviceConfig.ResourceGroupName, instanceName, nil)
	if err != nil {
		return fmt.Errorf("sending stop request: %w", err)
	}

	_, err = pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("waiting for the VM shutdown: %w", err)
	}

	logger.Printf("shutdown VM successfully: %s", instanceName)

	return nil
}

// Start the instance
func (p *azureProvider) start(ctx context.Context, instanceName string) error {

	// Create a new instance of the Virtual Machines client
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating VM client: %w", err)
	}

	// Start the instance
	pollerResponse, err := vmClient.BeginStart(ctx, p.serviceConfig.ResourceGroupName, instanceName, nil)
	if err != nil {
		return fmt.Errorf("sending stop request: %w", err)
	}

	_, err = pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("waiting for the VM startup: %w", err)
	}

	logger.Printf("started VM successfully: %s", instanceName)

	return nil
}

// Method to modify the userData of the VM
func (p *azureProvider) modifyInstanceUserData(ctx context.Context, instanceName string, userDataEnc string) error {

	// Create a new instance of the Virtual Machines client
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return fmt.Errorf("creating VM client: %w", err)
	}

	// Get VM details
	vm, err := vmClient.Get(ctx, p.serviceConfig.ResourceGroupName, instanceName, nil)
	if err != nil {
		return fmt.Errorf("getting VM details: %w", err)
	}

	// Note that we can only change the UserData and not the customData
	// Update UserData
	vm.VirtualMachine.Properties.UserData = &userDataEnc

	// Update VM
	pollerResponse, err := vmClient.BeginCreateOrUpdate(ctx, p.serviceConfig.ResourceGroupName, instanceName, vm.VirtualMachine, nil)
	if err != nil {
		return fmt.Errorf("sending update request: %w", err)
	}

	_, err = pollerResponse.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("waiting for the VM update: %w", err)
	}

	return nil

}

// Method to check podVM pool size and create new instances if needed
func (p *azureProvider) checkPodVmPoolSize(ctx context.Context, numInstances int) {

	// Check every 15 minutes
	checkInterval := 15 * time.Minute
	//filterString := fmt.Sprintf("startswith(name, '%s')", "podvm-ready")

	for {
		// Sleep in the beginning before doing the check
		time.Sleep(checkInterval)
		// Get the list of VMs with instance name prefix "podvm-ready" prefix and check the count
		// If the count is less than the required number of instances, create new instances
		// Get the count of VMs using the filter string
		//count, err := p.getInstanceCount(ctx, filterString)
		//if err != nil {
		//	logger.Printf("error getting pre created instance count: %v", err)
		//	continue
		//}
		// Check the length of the preCreatedInstances slice
		count := len(p.serviceConfig.PreCreatedInstances)
		// If the count is less than the required number of instances, create new instances
		if count < numInstances {
			// Re-initialise podVM pool size
			podVmPoolSize := numInstances - count
			if err := p.initializePodVmPool(ctx, podVmPoolSize); err != nil {
				logger.Printf("error initializing podVM pool: %v", err)
				continue
			}
		}
	}
}

func (p *azureProvider) getInstanceCount(ctx context.Context, filterString string) (int, error) {

	count := 0

	// Create a new instance of the Virtual Machines client
	vmClient, err := armcompute.NewVirtualMachinesClient(p.serviceConfig.SubscriptionId, p.azureClient, nil)
	if err != nil {
		return count, fmt.Errorf("creating VM client: %w", err)
	}

	// Create VirtualMachinesClientListOptions with the filter string
	listOpt := armcompute.VirtualMachinesClientListOptions{
		Filter: to.Ptr(filterString),
	}
	// Create a new pager with listOpt
	pager := vmClient.NewListPager(p.serviceConfig.ResourceGroupName, &listOpt)

	// Loop till pager.More() returns false
	for pager.More() {
		// Get the output of the pager
		page, err := pager.NextPage(ctx)
		if err != nil {
			// Log err and continue
			logger.Printf("error getting next page: %v", err)
			continue
		}
		// Count the number of VMs in the response
		count = count + len(page.VirtualMachineListResult.Value)
		// Log the VM names
		for _, vm := range page.VirtualMachineListResult.Value {
			logger.Printf("VM name: %s", *vm.Name)
		}

	}

	return count, nil
}

// Add method to destroy the precreated podVM pool
func (p *azureProvider) destroyPodVmPool(ctx context.Context) error {

	// For the instance.ID in the preCreatedInstances, delete the VM
	for _, instance := range p.serviceConfig.PreCreatedInstances {
		if err := p.DeleteInstance(ctx, instance.ID); err != nil {
			logger.Printf("error deleting pre created instance: %v", err)
			continue
		}
	}
	return nil

}
