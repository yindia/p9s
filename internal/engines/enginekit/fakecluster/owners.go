package fakecluster

import "k8s.io/apimachinery/pkg/runtime/schema"

// Workload GVRs for SeedOwned.
var (
	Pods         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	ReplicaSets  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	Deployments  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	StatefulSets = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	Jobs         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	CronJobs     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	Namespaces   = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	Events       = schema.GroupVersionResource{Version: "v1", Resource: "events"}
)

// DeploymentPods returns the metadata of a Deployment, its ReplicaSet and
// pods: the chain Pod → ReplicaSet → Deployment.
func DeploymentPods(ns, deploy, rs string, labels map[string]string, pods ...string) []Owned {
	out := []Owned{
		{GVR: Deployments, Kind: "Deployment", Namespace: ns, Name: deploy, Labels: labels},
		{GVR: ReplicaSets, Kind: "ReplicaSet", Namespace: ns, Name: rs, Owner: "Deployment/" + deploy, OwnerAPI: "apps/v1"},
	}
	for _, p := range pods {
		out = append(out, Owned{GVR: Pods, Kind: "Pod", Namespace: ns, Name: p, Owner: "ReplicaSet/" + rs, OwnerAPI: "apps/v1"})
	}
	return out
}

// Namespace returns a labelled namespace.
func Namespace(name string, labels map[string]string) Owned {
	return Owned{GVR: Namespaces, Kind: "Namespace", Name: name, Labels: labels}
}

// PlatformResources are what the health dashboard and the events watcher
// watch.
var PlatformResources = append(append(append(append(
	ResourcesFor("admissionregistration.k8s.io/v1",
		"validatingwebhookconfigurations:ValidatingWebhookConfiguration",
		"mutatingwebhookconfigurations:MutatingWebhookConfiguration"),
	ResourcesFor("discovery.k8s.io/v1", "endpointslices:EndpointSlice:ns")...),
	ResourcesFor("coordination.k8s.io/v1", "leases:Lease:ns")...),
	ResourcesFor("v1", "pods:Pod:ns")...),
	ResourcesFor("v1", "events:Event:ns")...)

// With appends extra resources not already in rs.
func With(rs []Resource, extra ...Resource) []Resource {
	out := append([]Resource(nil), rs...)
	for _, e := range extra {
		dup := false
		for _, r := range out {
			dup = dup || (r.GroupVersion == e.GroupVersion && r.Name == e.Name)
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}
