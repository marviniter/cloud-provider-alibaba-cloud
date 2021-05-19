package service

import (
	"context"
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/model"
	prvd "k8s.io/cloud-provider-alibaba-cloud/pkg/provider"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/util"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

// EndpointWithENI
// Currently, EndpointWithENI accept two kind of backend
// normal nodes of type []*v1.Node, and endpoints of type *v1.Endpoints
type TrafficPolicy string

const (
	// Local externalTrafficPolicy=Local
	LocalTrafficPolicy = TrafficPolicy("Local")
	// Cluster externalTrafficPolicy=Cluster
	ClusterTrafficPolicy = TrafficPolicy("Cluster")
	// ENI external traffic is forwarded to pod directly
	ENITrafficPolicy = TrafficPolicy("ENI")
)

type EndpointWithENI struct {
	// TrafficPolicy external traffic policy
	TrafficPolicy TrafficPolicy
	// Nodes
	// contains all the candidate nodes consider of LoadBalance Backends.
	// Cloud implementation has the right to make any filter on it.
	Nodes []v1.Node
	// Endpoints
	// It is the direct pod location information which cloud implementation
	// may needed for some kind of filtering. eg. direct ENI attach.
	Endpoints *v1.Endpoints
}

func (e *EndpointWithENI) setTrafficPolicy(reqCtx *RequestContext) {
	if isENIBackendType(reqCtx.Service) {
		e.TrafficPolicy = ENITrafficPolicy
		return
	}
	if isLocalModeService(reqCtx.Service) {
		e.TrafficPolicy = LocalTrafficPolicy
		return
	}
	e.TrafficPolicy = ClusterTrafficPolicy
	return
}

func NewVGroupManager(kubeClient client.Client, cloud prvd.Provider) *VGroupManager {
	return &VGroupManager{
		kubeClient: kubeClient,
		cloud:      cloud,
	}
}

type VGroupManager struct {
	kubeClient client.Client
	cloud      prvd.Provider
}

func (mgr *VGroupManager) BuildLocalModel(reqCtx *RequestContext, m *model.LoadBalancer) error {
	var vgs []model.VServerGroup
	nodes, err := getNodes(reqCtx.Ctx, mgr.kubeClient, reqCtx.Anno)
	if err != nil {
		return fmt.Errorf("get nodes error: %s", err.Error())
	}

	eps, err := getEndpoints(reqCtx.Ctx, mgr.kubeClient, reqCtx.Service)
	if err != nil {
		return fmt.Errorf("get endpoints error: %s", err.Error())
	}

	candidates := &EndpointWithENI{
		Nodes:     nodes,
		Endpoints: eps,
	}
	candidates.setTrafficPolicy(reqCtx)

	for _, port := range reqCtx.Service.Spec.Ports {
		vg, err := mgr.buildVGroupForServicePort(reqCtx, port, candidates)
		if err != nil {
			return fmt.Errorf("build vgroup for port %d error: %s", port.Port, err.Error())
		}
		vgs = append(vgs, vg)
	}
	m.VServerGroups = vgs
	return nil
}

func (mgr *VGroupManager) BuildRemoteModel(reqCtx *RequestContext, m *model.LoadBalancer) error {
	vgs, err := mgr.DescribeVServerGroups(reqCtx, m.LoadBalancerAttribute.LoadBalancerId)
	if err != nil {
		return fmt.Errorf("DescribeVServerGroups error: %s", err.Error())
	}
	m.VServerGroups = vgs
	return nil
}

func (mgr *VGroupManager) CreateVServerGroup(reqCtx *RequestContext, vg *model.VServerGroup, lbId string) error {
	return mgr.cloud.CreateVServerGroup(reqCtx.Ctx, vg, lbId)
}

func (mgr *VGroupManager) DeleteVServerGroup(reqCtx *RequestContext, vGroupId string) error {
	return mgr.cloud.DeleteVServerGroup(reqCtx.Ctx, vGroupId)
}

func (mgr *VGroupManager) UpdateVServerGroup(reqCtx *RequestContext, local, remote model.VServerGroup) error {
	add, del, update := diff(remote, local)
	if len(add) == 0 && len(del) == 0 && len(update) == 0 {
		klog.Infof("update: no backend need to be added for vgroupid")
	}
	if len(add) > 0 {
		if err := mgr.BatchAddVServerGroupBackendServers(reqCtx, local, add); err != nil {
			return err
		}
	}
	if len(del) > 0 {
		if err := mgr.BatchRemoveVServerGroupBackendServers(reqCtx, remote, del); err != nil {
			return err
		}
	}
	if len(update) > 0 {
		return mgr.BatchUpdateVServerGroupBackendServers(reqCtx, remote, update)
	}
	return nil
}

func (mgr *VGroupManager) DescribeVServerGroups(reqCtx *RequestContext, lbId string) ([]model.VServerGroup, error) {
	return mgr.cloud.DescribeVServerGroups(reqCtx.Ctx, lbId)
}

func (mgr *VGroupManager) BatchAddVServerGroupBackendServers(reqCtx *RequestContext, vGroup model.VServerGroup, add interface{}) error {
	return Batch(add, MAX_BACKEND_NUM,
		func(list []interface{}) error {
			additions, err := json.Marshal(list)
			if err != nil {
				return fmt.Errorf("error marshal backends: %s, %v", err.Error(), list)
			}
			klog.Infof("update: try to update vserver group[%s],"+
				" backend add[%s]", vGroup.NamedKey.Key(), string(additions))
			return mgr.cloud.AddVServerGroupBackendServers(reqCtx.Ctx, vGroup.VGroupId, string(additions))
		})
}

func (mgr *VGroupManager) BatchRemoveVServerGroupBackendServers(reqCtx *RequestContext, vGroup model.VServerGroup, del interface{}) error {
	return Batch(del, MAX_BACKEND_NUM,
		func(list []interface{}) error {
			deletions, err := json.Marshal(list)
			if err != nil {
				return fmt.Errorf("error marshal backends: %s, %v", err.Error(), list)
			}
			klog.Infof("update: try to update vserver group[%s],"+
				" backend del[%s]", vGroup.NamedKey.Key(), string(deletions))
			return mgr.cloud.RemoveVServerGroupBackendServers(reqCtx.Ctx, vGroup.VGroupId, string(deletions))
		})
}

func (mgr *VGroupManager) BatchUpdateVServerGroupBackendServers(reqCtx *RequestContext, vGroup model.VServerGroup, update interface{}) error {
	return Batch(update, MAX_BACKEND_NUM,
		func(list []interface{}) error {
			updateJson, err := json.Marshal(list)
			if err != nil {
				return fmt.Errorf("error marshal backends: %s, %v", err.Error(), list)
			}
			klog.Infof("update: try to update vserver group[%s],"+
				" backend update[%s]", vGroup.NamedKey.Key(), string(updateJson))
			return mgr.cloud.SetVServerGroupAttribute(reqCtx.Ctx, vGroup.VGroupId, string(updateJson))
		})
}

func diff(remote, local model.VServerGroup) (
	[]model.BackendAttribute, []model.BackendAttribute, []model.BackendAttribute) {

	var (
		addition  []model.BackendAttribute
		deletions []model.BackendAttribute
		updates   []model.BackendAttribute
	)

	for _, r := range remote.Backends {
		//// skip nodes which does not belong to the cluster
		//if isUserManagedNode(api.Description, v.NamedKey.Key()) {
		//	continue
		//}
		found := false
		for _, l := range local.Backends {
			if l.Type == "eni" {
				if r.ServerId == l.ServerId &&
					r.ServerIp == l.ServerIp {
					found = true
					break
				}
			} else {
				if r.ServerId == l.ServerId {
					found = true
					break
				}
			}

		}
		if !found {
			deletions = append(deletions, r)
		}
	}
	for _, l := range local.Backends {
		found := false
		for _, r := range remote.Backends {
			if l.Type == "eni" {
				if r.ServerId == l.ServerId &&
					r.ServerIp == l.ServerIp {
					found = true
					break
				}
			} else {
				if r.ServerId == l.ServerId {
					found = true
					break
				}
			}

		}
		if !found {
			addition = append(addition, l)
		}
	}
	for _, l := range local.Backends {
		for _, r := range remote.Backends {
			//if isUserManagedNode(api.Description, v.NamedKey.Key()) {
			//	continue
			//}
			if l.Type == "eni" {
				if l.ServerId == r.ServerId &&
					l.ServerIp == r.ServerIp &&
					l.Weight != r.Weight {
					updates = append(updates, l)
					break
				}
			} else {
				if l.ServerId == r.ServerId &&
					l.Weight != r.Weight {
					updates = append(updates, l)
					break
				}
			}
		}
	}
	return addition, deletions, updates
}

func getNodes(ctx context.Context, client client.Client, anno *AnnotationRequest) ([]v1.Node, error) {
	nodeList := v1.NodeList{}
	err := client.List(ctx, &nodeList)
	if err != nil {
		return nil, fmt.Errorf("get nodes error: %s", err.Error())
	}

	// 1. filter by label
	items := nodeList.Items
	if anno.Get(BackendLabel) != "" {
		items, err = filterOutByLabel(nodeList.Items, anno.Get(BackendLabel))
		if err != nil {
			return nil, fmt.Errorf("filter nodes by label error: %s", err.Error())
		}
	}

	var nodes []v1.Node
	for _, n := range items {
		// 2. filter unscheduled node
		if n.Spec.Unschedulable && anno.Get(RemoveUnscheduled) != "" {
			if anno.Get(RemoveUnscheduled) == string(model.OnFlag) {
				klog.Infof("ignore node %s with unschedulable condition", n.Name)
				continue
			}
		}

		// As of 1.6, we will taint the master, but not necessarily mark
		// it unschedulable. Recognize nodes labeled as master, and filter
		// them also, as we were doing previously.
		if _, isMaster := n.Labels[LabelNodeRoleMaster]; isMaster {
			continue
		}

		// ignore eci node condition check
		if label, ok := n.Labels["type"]; ok && label == ECINodeLabel {
			klog.Infof("ignoring eci node %v condition check", n.Name)
			nodes = append(nodes, n)
			continue
		}

		// If we have no info, don't accept
		if len(n.Status.Conditions) == 0 {
			continue
		}

		for _, cond := range n.Status.Conditions {
			// We consider the node for load balancing only when its NodeReady
			// condition status is ConditionTrue
			if cond.Type == v1.NodeReady &&
				cond.Status != v1.ConditionTrue {
				klog.Infof("ignoring node %v with %v condition "+
					"status %v", n.Name, cond.Type, cond.Status)
				continue
			}
		}

		nodes = append(nodes, n)
	}

	return nodes, nil
}

func filterOutByLabel(nodes []v1.Node, labels string) ([]v1.Node, error) {
	if labels == "" {
		return nodes, nil
	}
	var result []v1.Node
	lbl := strings.Split(labels, ",")
	var records []string
	for _, node := range nodes {
		found := true
		for _, v := range lbl {
			l := strings.Split(v, "=")
			if len(l) < 2 {
				return []v1.Node{}, fmt.Errorf("parse backend label: %s, [k1=v1,k2=v2]", v)
			}
			if nv, exist := node.Labels[l[0]]; !exist || nv != l[1] {
				found = false
				break
			}
		}
		if found {
			result = append(result, node)
			records = append(records, node.Name)
		}
	}
	klog.V(4).Infof("accept nodes backend labels[%s], %v", labels, records)
	return result, nil
}

func getEndpoints(ctx context.Context, client client.Client, svc *v1.Service) (*v1.Endpoints, error) {
	eps := &v1.Endpoints{}
	err := client.Get(ctx, util.NamespacedName(svc), eps)
	if err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return eps, fmt.Errorf("get endpoints %s from k8s error: %s", util.Key(svc), err.Error())
		}
		klog.Warningf("endpoint not found: %s", util.Key(svc))
	}
	return eps, nil
}

func (mgr *VGroupManager) buildVGroupForServicePort(reqCtx *RequestContext, port v1.ServicePort, candidates *EndpointWithENI) (model.VServerGroup, error) {
	vg := model.VServerGroup{
		NamedKey:    getVGroupNamedKey(reqCtx.Service, port),
		ServicePort: port,
	}
	vg.VGroupName = vg.NamedKey.Key()
	// build backends
	var (
		backends []model.BackendAttribute
		err      error
	)
	switch candidates.TrafficPolicy {
	case ENITrafficPolicy:
		reqCtx.Log.Infof("eni mode, build backends for %s", vg.NamedKey)
		backends, err = mgr.buildENIBackends(reqCtx, candidates, vg)
		if err != nil {
			return vg, err
		}
	case LocalTrafficPolicy:
		reqCtx.Log.Infof("local mode, build backends for %s", vg.NamedKey)
		backends, err = mgr.buildLocalBackends(reqCtx, candidates, vg)
		if err != nil {
			return vg, err
		}
	case ClusterTrafficPolicy:
		reqCtx.Log.Infof("cluster mode, build backends for %s", vg.NamedKey)
		backends, err = mgr.buildClusterBackends(reqCtx, candidates, vg)
		if err != nil {
			return vg, err
		}
	default:
		return vg, fmt.Errorf("not supported traffic policy [%s]", candidates.TrafficPolicy)
	}

	vg.Backends = backends
	return vg, nil
}

func setGenericBackendAttribute(candidates *EndpointWithENI, vgroup model.VServerGroup) []model.BackendAttribute {
	var backends []model.BackendAttribute

	for _, ep := range candidates.Endpoints.Subsets {
		var backendPort int
		for _, p := range ep.Ports {
			if p.Name == vgroup.ServicePort.Name {
				backendPort = int(p.Port)
				break
			}
		}

		for _, addr := range ep.Addresses {
			backends = append(backends, model.BackendAttribute{
				NodeName: addr.NodeName,
				ServerIp: addr.IP,
				// set backend port to targetPort by default
				// if backend type is ecs, update backend port to nodePort
				Port:        backendPort,
				Description: vgroup.VGroupName,
			})
		}
	}
	return backends
}

func (mgr *VGroupManager) buildENIBackends(reqCtx *RequestContext, candidates *EndpointWithENI, vgroup model.VServerGroup) ([]model.BackendAttribute, error) {
	if len(candidates.Endpoints.Subsets) == 0 {
		reqCtx.Log.Warningf("vgroup %s endpoint is nil", vgroup.VGroupName)
		return nil, nil
	}

	backends := setGenericBackendAttribute(candidates, vgroup)

	backends, err := updateENIBackends(mgr, backends)
	if err != nil {
		return backends, err
	}

	// set weight
	for i := range backends {
		backends[i].Weight = DEFAULT_SERVER_WEIGHT
	}
	return backends, nil
}

func (mgr *VGroupManager) buildLocalBackends(reqCtx *RequestContext, candidates *EndpointWithENI, vgroup model.VServerGroup) ([]model.BackendAttribute, error) {
	if len(candidates.Endpoints.Subsets) == 0 {
		reqCtx.Log.Warningf("vgroup %s endpoint is nil", vgroup.VGroupName)
		return nil, nil
	}

	initBackends := setGenericBackendAttribute(candidates, vgroup)
	var (
		ecsBackends, eciBackends []model.BackendAttribute
		err                      error
	)

	// filter ecs backends and eci backends
	// 1. add ecs backends. add pod located nodes.
	// Attention: will add duplicated ecs backends.
	for _, backend := range initBackends {
		if backend.NodeName == nil {
			return nil, fmt.Errorf("add ecs backends for service[%s] error, NodeName is nil for ip %s ",
				util.Key(reqCtx.Service), backend.ServerIp)
		}
		node := findNodeByNodeName(candidates.Nodes, *backend.NodeName)
		if node == nil {
			reqCtx.Log.Warningf("can not find correspond node %s for endpoint %s", *backend.NodeName, backend.ServerIp)
			continue
		}

		// check if the node is ECI
		if node.Labels["type"] == ECINodeLabel {
			eciBackends = append(eciBackends, backend)
			continue
		}

		if isExcludeNode(node) {
			continue
		}

		_, id, err := nodeFromProviderID(node.Spec.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("parse providerid: %s. "+
				"expected: ${regionid}.${nodeid}, %s", node.Spec.ProviderID, err.Error())
		}
		backend.ServerId = id
		backend.Type = model.ECSBackendType
		// for ECS backend type, port should be set to NodePort
		backend.Port = int(vgroup.ServicePort.NodePort)
		ecsBackends = append(ecsBackends, backend)
	}

	// 2. add eci backends
	if len(eciBackends) != 0 {
		reqCtx.Log.Infof("add eciBackends")
		eciBackends, err = updateENIBackends(mgr, eciBackends)
		if err != nil {
			return nil, fmt.Errorf("update eci backends error: %s", err.Error())
		}
	}

	backends := append(ecsBackends, eciBackends...)

	// 3. set weight
	backends = setWeightForLocalBackends(backends)

	// 4. remove duplicated ecs
	return remoteDuplicatedECS(backends), nil
}

func setWeightForLocalBackends(backends []model.BackendAttribute) []model.BackendAttribute {
	ecsPods := make(map[string]int)
	for _, b := range backends {
		ecsPods[b.ServerId] += 1
	}
	for i := range backends {
		backends[i].Weight = ecsPods[backends[i].ServerId]
	}
	return backends
}

func remoteDuplicatedECS(backends []model.BackendAttribute) []model.BackendAttribute {
	nodeMap := make(map[string]bool)
	var uniqBackends []model.BackendAttribute
	for _, backend := range backends {
		if _, ok := nodeMap[backend.ServerId]; ok {
			continue
		}
		nodeMap[backend.ServerId] = true
		uniqBackends = append(uniqBackends, backend)
	}
	return uniqBackends

}

func (mgr *VGroupManager) buildClusterBackends(reqCtx *RequestContext, candidates *EndpointWithENI, vgroup model.VServerGroup) ([]model.BackendAttribute, error) {
	if len(candidates.Endpoints.Subsets) == 0 {
		reqCtx.Log.Warningf("vgroup %s endpoint is nil", vgroup.VGroupName)
		return nil, nil
	}

	initBackends := setGenericBackendAttribute(candidates, vgroup)
	var (
		ecsBackends, eciBackends []model.BackendAttribute
		err                      error
	)

	// 1. add ecs backends. add all cluster nodes.
	for _, node := range candidates.Nodes {
		if isExcludeNode(&node) {
			continue
		}
		_, id, err := nodeFromProviderID(node.Spec.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("normal parse providerid: %s. "+
				"expected: ${regionid}.${nodeid}, %s", node.Spec.ProviderID, err.Error())
		}

		ecsBackends = append(
			ecsBackends,
			model.BackendAttribute{
				ServerId:    id,
				Weight:      DEFAULT_SERVER_WEIGHT,
				Port:        int(vgroup.ServicePort.NodePort),
				Type:        model.ECSBackendType,
				Description: vgroup.VGroupName,
			},
		)
	}

	// 2. add eci backends
	for _, backend := range initBackends {
		if backend.NodeName == nil {
			return nil, fmt.Errorf("add ecs backends for service[%s] error, NodeName is nil for ip %s ",
				util.Key(reqCtx.Service), backend.ServerIp)
		}
		node := findNodeByNodeName(candidates.Nodes, *backend.NodeName)
		if node == nil {
			reqCtx.Log.Warningf("can not find correspond node %s for endpoint %s", *backend.NodeName, backend.ServerIp)
			continue
		}

		// check if the node is ECI
		if node.Labels["type"] == ECINodeLabel {
			eciBackends = append(eciBackends, backend)
			continue
		}
	}

	if len(eciBackends) != 0 {
		eciBackends, err = updateENIBackends(mgr, eciBackends)
		if err != nil {
			return nil, fmt.Errorf("update eci backends error: %s", err.Error())
		}
	}

	backends := append(ecsBackends, eciBackends...)

	// 3. set weight
	for i := range backends {
		backends[i].Weight = DEFAULT_SERVER_WEIGHT
	}

	return backends, nil
}

func updateENIBackends(mgr *VGroupManager, backends []model.BackendAttribute) ([]model.BackendAttribute, error) {
	vpcId, err := mgr.cloud.VpcID()
	if err != nil {
		return nil, fmt.Errorf("get vpc id from metadata error:%s", err.Error())
	}
	var (
		ips       []string
		nextToken string
	)
	for _, b := range backends {
		ips = append(ips, b.ServerIp)
	}
	ecis := make(map[string]string)
	for {
		resp, err := mgr.cloud.DescribeNetworkInterfaces(vpcId, &ips, nextToken)
		if err != nil {
			return nil, fmt.Errorf("call DescribeNetworkInterfaces: %s", err.Error())
		}
		for _, ip := range ips {
			for _, eni := range resp.NetworkInterfaceSets.NetworkInterfaceSet {
				for _, privateIp := range eni.PrivateIpSets.PrivateIpSet {
					if ip == privateIp.PrivateIpAddress {
						ecis[ip] = eni.NetworkInterfaceId
					}
				}
			}
		}
		if resp.NextToken == "" {
			break
		}
		nextToken = resp.NextToken
	}

	for i := range backends {
		eniid, ok := ecis[backends[i].ServerIp]
		if !ok {
			return nil, fmt.Errorf("can not find eniid for ip %s in vpc %s", backends[i].ServerIp, vpcId)
		}
		// for ENI backend type, port should be set to targetPort (default value), no need to update
		backends[i].ServerId = eniid
		backends[i].Type = model.ENIBackendType
	}
	return backends, nil
}
