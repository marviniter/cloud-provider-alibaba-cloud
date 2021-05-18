package service

import (
	"encoding/json"
	"fmt"
	ctx2 "k8s.io/cloud-provider-alibaba-cloud/pkg/context"
	"k8s.io/cloud-provider-alibaba-cloud/pkg/model"
	prvd "k8s.io/cloud-provider-alibaba-cloud/pkg/provider"
	"k8s.io/klog"
	"strconv"
)

func NewLoadBalancerManager(cloud prvd.Provider) *LoadBalancerManager {
	return &LoadBalancerManager{
		cloud: cloud,
	}
}

type LoadBalancerManager struct {
	cloud prvd.Provider
}

func (mgr *LoadBalancerManager) Find(reqCtx *RequestContext, mdl *model.LoadBalancer) error {
	// 1. set load balancer id
	if reqCtx.Anno.Get(LoadBalancerId) != "" {
		mdl.LoadBalancerAttribute.LoadBalancerId = reqCtx.Anno.Get(LoadBalancerId)
	}

	// 2. set default loadbalancer name
	// it's safe to set loadbalancer name which will be overwritten in FindLoadBalancer func
	mdl.LoadBalancerAttribute.LoadBalancerName = reqCtx.Anno.GetDefaultLoadBalancerName()

	// 3. set default loadbalancer tag
	mdl.LoadBalancerAttribute.Tags = reqCtx.Anno.GetDefaultTags()
	return mgr.cloud.FindLoadBalancer(reqCtx.Ctx, mdl)
}

func (mgr *LoadBalancerManager) Create(reqCtx *RequestContext, local *model.LoadBalancer) error {
	setModelDefaultValue(local, reqCtx.Anno)
	err := mgr.cloud.CreateLoadBalancer(reqCtx.Ctx, local)
	if err != nil {
		return fmt.Errorf("create slb error: %s", err.Error())
	}

	tags, err := json.Marshal(local.LoadBalancerAttribute.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags error: %s", err.Error())
	}
	return mgr.cloud.AddTags(reqCtx.Ctx, local.LoadBalancerAttribute.LoadBalancerId, string(tags))
}

func (mgr *LoadBalancerManager) Delete(reqCtx *RequestContext, remote *model.LoadBalancer) error {
	if remote.LoadBalancerAttribute.LoadBalancerId == "" {
		return nil
	}

	// set delete protection off
	if remote.LoadBalancerAttribute.DeleteProtection == model.OnFlag {
		if err := mgr.cloud.SetLoadBalancerDeleteProtection(
			reqCtx.Ctx,
			remote.LoadBalancerAttribute.LoadBalancerId,
			string(model.OffFlag),
		); err != nil {
			return fmt.Errorf("error to set slb id [%s] delete protection off, svc [%s], err: %s",
				remote.LoadBalancerAttribute.LoadBalancerId, remote.NamespacedName, err.Error())
		}
	}

	return mgr.cloud.DeleteLoadBalancer(reqCtx.Ctx, remote)

}

func (mgr *LoadBalancerManager) Update(reqCtx *RequestContext, local, remote *model.LoadBalancer) error {
	lbId := remote.LoadBalancerAttribute.LoadBalancerId
	klog.Infof("found load balancer [%s], try to update load balancer attribute", lbId)

	if local.LoadBalancerAttribute.MasterZoneId != "" &&
		local.LoadBalancerAttribute.MasterZoneId != remote.LoadBalancerAttribute.MasterZoneId {
		return fmt.Errorf("alicloud: can not change LoadBalancer master zone id once created")
	}
	if local.LoadBalancerAttribute.SlaveZoneId != "" &&
		local.LoadBalancerAttribute.SlaveZoneId != remote.LoadBalancerAttribute.SlaveZoneId {
		return fmt.Errorf("alicloud: can not change LoadBalancer slave zone id once created")
	}
	if local.LoadBalancerAttribute.AddressType != "" &&
		local.LoadBalancerAttribute.AddressType != remote.LoadBalancerAttribute.AddressType {
		return fmt.Errorf("alicloud: can not change LoadBalancer AddressType once created. delete and retry")
	}
	if !equalsAddressIPVersion(local.LoadBalancerAttribute.AddressIPVersion, remote.LoadBalancerAttribute.AddressIPVersion) {
		return fmt.Errorf("alicloud: can not change LoadBalancer AddressIPVersion once created")
	}
	if local.LoadBalancerAttribute.ResourceGroupId != "" &&
		local.LoadBalancerAttribute.ResourceGroupId != remote.LoadBalancerAttribute.ResourceGroupId {
		return fmt.Errorf("alicloud: can not change ResourceGroupId once created")
	}

	// update chargeType & bandwidth
	needUpdate, charge, bandwidth := false, remote.LoadBalancerAttribute.InternetChargeType, remote.LoadBalancerAttribute.Bandwidth
	if local.LoadBalancerAttribute.InternetChargeType != "" &&
		local.LoadBalancerAttribute.InternetChargeType != remote.LoadBalancerAttribute.InternetChargeType {
		needUpdate = true
		charge = local.LoadBalancerAttribute.InternetChargeType
		klog.Infof("internet chargeType changed([%s] -> [%s]), update loadbalancer [%s]",
			remote.LoadBalancerAttribute.InternetChargeType, local.LoadBalancerAttribute.InternetChargeType, lbId)
	}
	if local.LoadBalancerAttribute.Bandwidth != 0 &&
		local.LoadBalancerAttribute.Bandwidth != remote.LoadBalancerAttribute.Bandwidth &&
		local.LoadBalancerAttribute.InternetChargeType == model.PayByBandwidth {
		needUpdate = true
		bandwidth = local.LoadBalancerAttribute.Bandwidth
		klog.Infof("bandwidth changed([%d] -> [%d]), update loadbalancer[%s]",
			remote.LoadBalancerAttribute.Bandwidth, local.LoadBalancerAttribute.Bandwidth, lbId)
	}
	if needUpdate {
		if remote.LoadBalancerAttribute.AddressType == model.InternetAddressType {
			klog.Infof("modify loadbalancer: chargeType=%s, bandwidth=%d", charge, bandwidth)
			return mgr.cloud.ModifyLoadBalancerInternetSpec(reqCtx.Ctx, lbId, string(charge), bandwidth)
		} else {
			klog.Warningf("only internet loadbalancer is allowed to modify bandwidth and pay type")
		}
	}

	// update instance spec
	if local.LoadBalancerAttribute.LoadBalancerSpec != "" &&
		local.LoadBalancerAttribute.LoadBalancerSpec != remote.LoadBalancerAttribute.LoadBalancerSpec {
		klog.Infof("alicloud: loadbalancerSpec changed ([%s] -> [%s]), update loadbalancer [%s]",
			remote.LoadBalancerAttribute.LoadBalancerSpec, local.LoadBalancerAttribute.LoadBalancerSpec, lbId)
		return mgr.cloud.ModifyLoadBalancerInstanceSpec(reqCtx.Ctx, lbId, string(local.LoadBalancerAttribute.LoadBalancerSpec))
	}

	// update slb delete protection
	if local.LoadBalancerAttribute.DeleteProtection != "" &&
		local.LoadBalancerAttribute.DeleteProtection != remote.LoadBalancerAttribute.DeleteProtection {
		klog.Infof("delete protection changed([%s] -> [%s]), update loadbalancer [%s]",
			remote.LoadBalancerAttribute.DeleteProtection, local.LoadBalancerAttribute.DeleteProtection, lbId)
		return mgr.cloud.SetLoadBalancerDeleteProtection(reqCtx.Ctx, lbId, string(local.LoadBalancerAttribute.DeleteProtection))
	}

	// update modification protection
	if local.LoadBalancerAttribute.ModificationProtectionStatus != "" &&
		local.LoadBalancerAttribute.ModificationProtectionStatus != remote.LoadBalancerAttribute.ModificationProtectionStatus {
		klog.Infof("alicloud: loadbalancer modification protection changed([%s] -> [%s]) changed, update loadbalancer [%s]",
			remote.LoadBalancerAttribute.ModificationProtectionStatus, local.LoadBalancerAttribute.ModificationProtectionStatus,
			remote.LoadBalancerAttribute.LoadBalancerId)
		return mgr.cloud.SetLoadBalancerModificationProtection(reqCtx.Ctx, lbId, string(local.LoadBalancerAttribute.ModificationProtectionStatus))
	}

	// update slb name
	// only user defined slb or slb which has "kubernetes.do.not.delete" tag can update name
	if local.LoadBalancerAttribute.LoadBalancerName != "" &&
		local.LoadBalancerAttribute.LoadBalancerName != remote.LoadBalancerAttribute.LoadBalancerName {
		//if isLoadBalancerHasTag(tags) || isUserDefinedLoadBalancer(service) {
		//	klog.Infof("alicloud: LoadBalancer name (%s -> %s) changed, update loadbalancer [%s]",
		//		remote.LoadBalancerAttribute.LoadBalancerName, local.LoadBalancerAttribute.LoadBalancerName, lbId)
		//	if err := slbClient.SetLoadBalancerName(context, lbId, local.LoadBalancerAttribute.LoadBalancerName); err != nil {
		//		return err
		//	}
		//}
		return mgr.cloud.SetLoadBalancerName(reqCtx.Ctx, lbId, local.LoadBalancerAttribute.LoadBalancerName)
	}
	return nil
}

// Build build load balancer attribute for local model
func (mgr *LoadBalancerManager) BuildLocalModel(reqCtx *RequestContext, mdl *model.LoadBalancer) error {
	mdl.LoadBalancerAttribute.AddressType = model.AddressType(reqCtx.Anno.Get(AddressType))
	mdl.LoadBalancerAttribute.InternetChargeType = model.InternetChargeType(reqCtx.Anno.Get(ChargeType))
	bandwidth := reqCtx.Anno.Get(Bandwidth)
	if bandwidth != "" {
		i, err := strconv.Atoi(bandwidth)
		if err != nil &&
			mdl.LoadBalancerAttribute.InternetChargeType == model.PayByBandwidth {
			return fmt.Errorf("bandwidth must be integer, got [%s], error: %s", bandwidth, err.Error())
		}
		mdl.LoadBalancerAttribute.Bandwidth = i
	}
	if reqCtx.Anno.Get(LoadBalancerId) != "" {
		mdl.LoadBalancerAttribute.LoadBalancerId = reqCtx.Anno.Get(LoadBalancerId)
		mdl.LoadBalancerAttribute.IsUserManaged = true
	}
	mdl.LoadBalancerAttribute.LoadBalancerName = reqCtx.Anno.Get(LoadBalancerName)
	mdl.LoadBalancerAttribute.VSwitchId = reqCtx.Anno.Get(VswitchId)
	mdl.LoadBalancerAttribute.MasterZoneId = reqCtx.Anno.Get(MasterZoneID)
	mdl.LoadBalancerAttribute.SlaveZoneId = reqCtx.Anno.Get(SlaveZoneID)
	mdl.LoadBalancerAttribute.LoadBalancerSpec = model.LoadBalancerSpecType(reqCtx.Anno.Get(Spec))
	mdl.LoadBalancerAttribute.ResourceGroupId = reqCtx.Anno.Get(ResourceGroupId)
	mdl.LoadBalancerAttribute.AddressIPVersion = model.AddressIPVersionType(reqCtx.Anno.Get(IPVersion))
	mdl.LoadBalancerAttribute.DeleteProtection = model.FlagType(reqCtx.Anno.Get(DeleteProtection))
	mdl.LoadBalancerAttribute.ModificationProtectionStatus = model.ModificationProtectionType(reqCtx.Anno.Get(ModificationProtection))
	return nil
}

func (mgr *LoadBalancerManager) BuildRemoteModel(reqCtx *RequestContext, mdl *model.LoadBalancer) error {
	return mgr.Find(reqCtx, mdl)
}

func equalsAddressIPVersion(local, remote model.AddressIPVersionType) bool {
	if local == "" {
		local = model.IPv4
	}

	if remote == "" {
		remote = model.IPv4
	}
	return local == remote
}

func setModelDefaultValue(mdl *model.LoadBalancer, anno *AnnotationRequest) {
	if mdl.LoadBalancerAttribute.AddressType == "" {
		mdl.LoadBalancerAttribute.AddressType = model.AddressType(anno.GetDefaultValue(AddressType))
	}

	if mdl.LoadBalancerAttribute.LoadBalancerName == "" {
		mdl.LoadBalancerAttribute.LoadBalancerName = anno.GetDefaultLoadBalancerName()
	}

	// TODO ecs模式下获取vpc id & vsw id
	if mdl.LoadBalancerAttribute.AddressType == model.IntranetAddressType {
		mdl.LoadBalancerAttribute.VpcId = ctx2.CFG.Global.VpcID
		if mdl.LoadBalancerAttribute.VSwitchId == "" {
			mdl.LoadBalancerAttribute.VSwitchId = ctx2.CFG.Global.VswitchID
		}
	}

	if mdl.LoadBalancerAttribute.LoadBalancerSpec == "" {
		mdl.LoadBalancerAttribute.LoadBalancerSpec = model.LoadBalancerSpecType(anno.GetDefaultValue(Spec))
	}

	if mdl.LoadBalancerAttribute.DeleteProtection == "" {
		mdl.LoadBalancerAttribute.DeleteProtection = model.FlagType(anno.GetDefaultValue(DeleteProtection))
	}

	if mdl.LoadBalancerAttribute.ModificationProtectionStatus == "" {
		mdl.LoadBalancerAttribute.ModificationProtectionStatus = model.ModificationProtectionType(anno.GetDefaultValue(ModificationProtection))
		mdl.LoadBalancerAttribute.ModificationProtectionReason = model.ModificationProtectionReason
	}

	mdl.LoadBalancerAttribute.Tags = append(mdl.LoadBalancerAttribute.Tags, anno.GetDefaultTags()...)
}
