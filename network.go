/*
Package libnetwork provides basic fonctionalities and extension points to
create network namespaces and allocate interfaces for containers to use.

// Create a new controller instance
controller := libnetwork.New()

options := options.Generic{}
// Create a network for containers to join.
network, err := controller.NewNetwork("simplebridge", "network1", options)
if err != nil {
	return
}

// For a new container: create a sandbox instance (providing a unique key).
// For linux it is a filesystem path
networkPath := "/var/lib/docker/.../4d23e"
networkNamespace, err := sandbox.NewSandbox(networkPath)
if err != nil {
	return
}

// For each new container: allocate IP and interfaces. The returned network
// settings will be used for container infos (inspect and such), as well as
// iptables rules for port publishing.
_, sinfo, err := network.CreateEndpoint("Endpoint1", networkNamespace.Key(), "")
if err != nil {
	return
}

// Add interfaces to the namespace.
for _, iface := range sinfo.Interfaces {
	if err := networkNamespace.AddInterface(iface); err != nil {
		return
	}
}

// Set the gateway IP
if err := networkNamespace.SetGateway(sinfo.Gateway); err != nil {
	return
}
*/
package libnetwork

import (
	"fmt"
	"sync"

	"github.com/docker/docker/pkg/common"
	"github.com/docker/libnetwork/driverapi"
)

// NetworkController provides the interface for controller instance which manages
// networks.
type NetworkController interface {
	// Create a new network. The options parameter carry driver specific options.
	// Labels support will be added in the near future.
	NewNetwork(networkType, name string, options interface{}) (Network, error)
}

// A Network represents a logical connectivity zone that containers may
// ulteriorly join using the Link method. A Network is managed by a specific
// driver.
type Network interface {
	// A user chosen name for this network.
	Name() string

	// A system generated id for this network.
	ID() string

	// The type of network, which corresponds to its managing driver.
	Type() string

	// Create a new endpoint to this network symbolically identified by the
	// specified unique name. The options parameter carry driver specific options.
	// Labels support will be added in the near future.
	CreateEndpoint(name string, sboxKey string, options interface{}) (Endpoint, *driverapi.SandboxInfo, error)

	// Delete the network.
	Delete() error
}

// Endpoint represents a logical connection between a network and a sandbox.
type Endpoint interface {
	// Delete endpoint.
	Delete() error
}

type endpoint struct {
	name        string
	id          driverapi.UUID
	network     *network
	sandboxInfo *driverapi.SandboxInfo
}

type network struct {
	ctrlr       *controller
	name        string
	networkType string
	id          driverapi.UUID
	endpoints   map[driverapi.UUID]*endpoint
	sync.Mutex
}

type networkTable map[driverapi.UUID]*network

type controller struct {
	networks networkTable
	drivers  driverTable
	sync.Mutex
}

// New creates a new instance of network controller.
func New() NetworkController {
	return &controller{networkTable{}, enumerateDrivers(), sync.Mutex{}}
}

// NewNetwork creates a new network of the specified networkType. The options
// are driver specific and modeled in a generic way.
func (c *controller) NewNetwork(networkType, name string, options interface{}) (Network, error) {
	network := &network{name: name, networkType: networkType}
	network.id = driverapi.UUID(common.GenerateRandomID())
	network.ctrlr = c

	d, ok := c.drivers[networkType]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", networkType)
	}

	if err := d.CreateNetwork(network.id, options); err != nil {
		return nil, err
	}

	c.Lock()
	c.networks[network.id] = network
	c.Unlock()

	return network, nil
}

func (n *network) Name() string {
	return n.name
}

func (n *network) ID() string {
	return string(n.id)
}

func (n *network) Type() string {
	return n.networkType
}

func (n *network) Delete() error {
	var err error

	d, ok := n.ctrlr.drivers[n.networkType]
	if !ok {
		return fmt.Errorf("unknown driver %q", n.networkType)
	}

	n.ctrlr.Lock()
	_, ok = n.ctrlr.networks[n.id]
	if !ok {
		n.ctrlr.Unlock()
		return fmt.Errorf("unknown network %s id %s", n.name, n.id)
	}

	n.Lock()
	numEps := len(n.endpoints)
	n.Unlock()
	if numEps != 0 {
		n.ctrlr.Unlock()
		return fmt.Errorf("network %s has active endpoints", n.id)
	}

	delete(n.ctrlr.networks, n.id)
	n.ctrlr.Unlock()
	defer func() {
		if err != nil {
			n.ctrlr.Lock()
			n.ctrlr.networks[n.id] = n
			n.ctrlr.Unlock()
		}
	}()

	err = d.DeleteNetwork(n.id)
	return err
}

func (n *network) CreateEndpoint(name string, sboxKey string, options interface{}) (Endpoint, *driverapi.SandboxInfo, error) {
	ep := &endpoint{name: name}
	ep.id = driverapi.UUID(common.GenerateRandomID())
	ep.network = n

	d, ok := n.ctrlr.drivers[n.networkType]
	if !ok {
		return nil, nil, fmt.Errorf("unknown driver %q", n.networkType)
	}

	sinfo, err := d.CreateEndpoint(n.id, ep.id, sboxKey, options)
	if err != nil {
		return nil, nil, err
	}

	ep.sandboxInfo = sinfo
	n.Lock()
	n.endpoints[ep.id] = ep
	n.Unlock()
	return ep, sinfo, nil
}

func (ep *endpoint) Delete() error {
	var err error

	d, ok := ep.network.ctrlr.drivers[ep.network.networkType]
	if !ok {
		return fmt.Errorf("unknown driver %q", ep.network.networkType)
	}

	n := ep.network
	n.Lock()
	_, ok = n.endpoints[ep.id]
	if !ok {
		n.Unlock()
		return fmt.Errorf("unknown endpoint %s id %s", ep.name, ep.id)
	}

	delete(n.endpoints, ep.id)
	n.Unlock()
	defer func() {
		if err != nil {
			n.Lock()
			n.endpoints[ep.id] = ep
			n.Unlock()
		}
	}()

	err = d.DeleteEndpoint(n.id, ep.id)
	return err
}
