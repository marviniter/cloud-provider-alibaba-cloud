package prvd

import (
	"context"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	v1 "k8s.io/api/core/v1"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/context/node"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/model"
	"time"
)

type DetailECS struct {
	// ImageID alibaba image id
	ImageID string
}

type Provider interface {
	IMetaData
	Instance
	Route
	ILoadBalancer
	PrivateZone
}

type RoleAuth struct {
	AccessKeyId     string
	AccessKeySecret string
	Expiration      time.Time
	SecurityToken   string
	LastUpdated     time.Time
	Code            string
}

// IMetaData metadata interface
type IMetaData interface {
	// values from metadata server
	HostName() (string, error)
	ImageID() (string, error)
	InstanceID() (string, error)
	Mac() (string, error)
	NetworkType() (string, error)
	OwnerAccountID() (string, error)
	PrivateIPv4() (string, error)
	Region() (string, error)
	SerialNumber() (string, error)
	SourceAddress() (string, error)
	VpcCIDRBlock() (string, error)
	VpcID() (string, error)
	VswitchCIDRBlock() (string, error)
	Zone() (string, error)
	NTPConfigServers() ([]string, error)
	RoleName() (string, error)
	RamRoleToken(role string) (RoleAuth, error)
	VswitchID() (string, error)
	// values from cloud config file
	ClusterID() string
}

// NodeAttribute node attribute from cloud instance
type NodeAttribute struct {
	InstanceID   string
	Addresses    []v1.NodeAddress
	InstanceType string
	Zone         string
	Region       string
}

type Instance interface {
	ListInstances(ctx *node.NodeContext, ids []string) (map[string]*NodeAttribute, error)
	SetInstanceTags(ctx *node.NodeContext, id string, tags map[string]string) error
	// DescribeNetworkInterfaces query one or more elastic network interfaces (ENIs)
	DescribeNetworkInterfaces(vpcId string, ips *[]string, nextToken string) (*ecs.DescribeNetworkInterfacesResponse, error)
}

type Route interface {
	CreateRoute()
	DeleteRoute()
	ListRoute()
}

type ILoadBalancer interface {
	// LoadBalancer
	FindLoadBalancer(ctx context.Context, mdl *model.LoadBalancer) error
	CreateLoadBalancer(ctx context.Context, mdl *model.LoadBalancer) error
	DescribeLoadBalancer(ctx context.Context, mdl *model.LoadBalancer) error
	DeleteLoadBalancer(ctx context.Context, mdl *model.LoadBalancer) error
	ModifyLoadBalancerInstanceSpec(ctx context.Context, lbId string, spec string) error
	SetLoadBalancerDeleteProtection(ctx context.Context, lbId string, flag string) error
	SetLoadBalancerName(ctx context.Context, lbId string, name string) error
	ModifyLoadBalancerInternetSpec(ctx context.Context, lbId string, chargeType string, bandwidth int) error
	SetLoadBalancerModificationProtection(ctx context.Context, lbId string, flag string) error
	AddTags(ctx context.Context, lbId string, tags string) error

	// Listener
	DescribeLoadBalancerListeners(ctx context.Context, lbId string) ([]model.ListenerAttribute, error)
	StartLoadBalancerListener(ctx context.Context, lbId string, port int) error
	StopLoadBalancerListener(ctx context.Context, lbId string, port int) error
	DeleteLoadBalancerListener(ctx context.Context, lbId string, port int) error
	CreateLoadBalancerTCPListener(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	SetLoadBalancerTCPListenerAttribute(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	CreateLoadBalancerUDPListener(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	SetLoadBalancerUDPListenerAttribute(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	CreateLoadBalancerHTTPListener(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	SetLoadBalancerHTTPListenerAttribute(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	CreateLoadBalancerHTTPSListener(ctx context.Context, lbId string, listener model.ListenerAttribute) error
	SetLoadBalancerHTTPSListenerAttribute(ctx context.Context, lbId string, listener model.ListenerAttribute) error

	// VServerGroup
	DescribeVServerGroups(ctx context.Context, lbId string) ([]model.VServerGroup, error)
	CreateVServerGroup(ctx context.Context, vg *model.VServerGroup, lbId string) error
	DescribeVServerGroupAttribute(ctx context.Context, vGroupId string) (model.VServerGroup, error)
	DeleteVServerGroup(ctx context.Context, vGroupId string) error
	AddVServerGroupBackendServers(ctx context.Context, vGroupId string, backends string) error
	RemoveVServerGroupBackendServers(ctx context.Context, vGroupId string, backends string) error
	SetVServerGroupAttribute(ctx context.Context, vGroupId string, backends string) error
	ModifyVServerGroupBackendServers(ctx context.Context, vGroupId string, old string, new string) error
}
