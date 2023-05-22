package crdsynchronizer

import (
	"context"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/pkg/sharedcli/ratelimiterflag"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ControllerName is the controller name that will be used when reporting events.
const ControllerName = "crd-synchronizer"

const clusterPropagationPolicyName = "serviceexport-policy"

type eventType int

const (
	ensure eventType = iota
	remove
)

// CRDSynchronizer is to sync ServiceExport CRD into member clusters.
type CRDSynchronizer struct {
	client.Client
	EventRecorder      record.EventRecorder
	RateLimiterOptions ratelimiterflag.Options
}

// Reconcile performs a full reconciliation for the object referred to by the Request.
// The Controller will requeue the Request to be processed again if an error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *CRDSynchronizer) Reconcile(ctx context.Context, req controllerruntime.Request) (controllerruntime.Result, error) {
	klog.V(4).InfoS("ServiceExport CRD synchronizer sync with cluster", "name", req.Name)

	cluster := &clusterv1alpha1.Cluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return r.syncClusterPropagationPolicy(ctx, cluster.Name, remove)
		}
		return controllerruntime.Result{}, err
	}

	if !cluster.DeletionTimestamp.IsZero() {
		return r.syncClusterPropagationPolicy(ctx, cluster.Name, remove)
	}

	return r.syncClusterPropagationPolicy(ctx, cluster.Name, ensure)
}

func (r *CRDSynchronizer) syncClusterPropagationPolicy(ctx context.Context, clusterName string, t eventType) (controllerruntime.Result, error) {
	policy := &policyv1alpha1.ClusterPropagationPolicy{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: clusterPropagationPolicyName}, policy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.createClusterPropagationPolicy(ctx)
		}
		klog.ErrorS(err, "failed to get clusterPropagationPolicy", "name", clusterPropagationPolicyName)
		return controllerruntime.Result{}, err
	}

	index := 0
	clusters := policy.Spec.Placement.ClusterAffinity.ClusterNames
	for ; index < len(clusters); index++ {
		if clusters[index] == clusterName {
			break
		}
	}

	switch t {
	case ensure:
		if index < len(clusters) {
			// target cluster have been added to cpp clusterNames
			klog.V(4).InfoS("no need to update clusterPropagationPolicy", "name", clusterPropagationPolicyName)
			return controllerruntime.Result{}, nil
		}
		clusters = append(clusters, clusterName)
	case remove:
		if index >= len(clusters) {
			// target cluster have been removed form cpp clusterNames
			klog.V(4).InfoS("no need to update clusterPropagationPolicy", "name", clusterPropagationPolicyName)
			return controllerruntime.Result{}, nil
		}
		clusters = append(clusters[:index], clusters[index+1:]...)
	}

	policy.Spec.Placement.ClusterAffinity.ClusterNames = clusters
	err = r.Client.Update(ctx, policy)
	if err != nil {
		klog.ErrorS(err, "failed to update clusterPropagationPolicy", "name", clusterPropagationPolicyName)
		return controllerruntime.Result{}, err
	}
	klog.V(4).InfoS("success to update clusterPropagationPolicy", "name", clusterPropagationPolicyName)
	return controllerruntime.Result{}, nil
}

func (r *CRDSynchronizer) createClusterPropagationPolicy(ctx context.Context) (controllerruntime.Result, error) {
	clusters := &clusterv1alpha1.ClusterList{}
	err := r.Client.List(ctx, clusters)
	if err != nil {
		klog.ErrorS(err, "failed to list clusters")
		return controllerruntime.Result{}, err
	}

	clusterNames := make([]string, len(clusters.Items))
	for index, cluster := range clusters.Items {
		clusterNames[index] = cluster.Name
	}

	policy := clusterPropagationPolicy(clusterNames)
	err = r.Client.Create(ctx, policy)
	if err != nil {
		klog.ErrorS(err, "failed to create clusterPropagationPolicy", "name", clusterPropagationPolicyName)
		return controllerruntime.Result{}, err
	}
	return controllerruntime.Result{}, nil
}

func clusterPropagationPolicy(clusters []string) *policyv1alpha1.ClusterPropagationPolicy {
	return &policyv1alpha1.ClusterPropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterPropagationPolicyName,
		},
		Spec: policyv1alpha1.PropagationSpec{
			ResourceSelectors: []policyv1alpha1.ResourceSelector{
				{
					APIVersion: "apiextensions.k8s.io/v1",
					Kind:       "CustomResourceDefinition",
					Name:       "serviceexports.multicluster.x-k8s.io",
				}},
			Placement: policyv1alpha1.Placement{
				ClusterAffinity: &policyv1alpha1.ClusterAffinity{
					ClusterNames: clusters,
				}}}}
}

// SetupWithManager creates a controller and register to controller manager.
func (r *CRDSynchronizer) SetupWithManager(_ context.Context, mgr controllerruntime.Manager) error {
	clusterFilter := predicate.Funcs{
		CreateFunc: func(event event.CreateEvent) bool { return true },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return !equality.Semantic.DeepEqual(updateEvent.ObjectOld.GetDeletionTimestamp().IsZero(),
				updateEvent.ObjectNew.GetDeletionTimestamp().IsZero())
		},
		DeleteFunc:  func(deleteEvent event.DeleteEvent) bool { return true },
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}

	cppHandlerFn := handler.MapFunc(
		func(object client.Object) []reconcile.Request {
			// return a fictional cluster, triggering to reconcile to recreate the cpp.
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{Name: "no-exist-cluster"}},
			}
		},
	)
	cppFilter := builder.WithPredicates(predicate.Funcs{
		CreateFunc: func(event event.CreateEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			return deleteEvent.Object.GetName() == clusterPropagationPolicyName
		},
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	})

	return controllerruntime.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.Cluster{}).
		Watches(&source.Kind{Type: &policyv1alpha1.ClusterPropagationPolicy{}},
			handler.EnqueueRequestsFromMapFunc(cppHandlerFn), cppFilter).
		WithEventFilter(clusterFilter).
		WithOptions(controller.Options{
			RateLimiter: ratelimiterflag.DefaultControllerRateLimiter(r.RateLimiterOptions),
		}).
		Complete(r)
}
