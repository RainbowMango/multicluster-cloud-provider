package indexes

import (
	"context"

	networkingv1alpha1 "github.com/karmada-io/karmada/pkg/apis/networking/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// IndexKeyServiceRefName is index key for services referenced by MultiClusterIngress.
	IndexKeyServiceRefName = "mci.serviceRef.name"
)

// SetupIndexesForMCI setups Indexes for MultiClusterIngress object.
func SetupIndexesForMCI(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := fieldIndexer.IndexField(ctx, &networkingv1alpha1.MultiClusterIngress{}, IndexKeyServiceRefName,
		func(object client.Object) []string {
			return BuildServiceRefIndexes(object.(*networkingv1alpha1.MultiClusterIngress))
		}); err != nil {
		return err
	}
	return nil
}

// BuildServiceRefIndexes returns the service refs in the MultiClusterIngress object.
func BuildServiceRefIndexes(mci *networkingv1alpha1.MultiClusterIngress) []string {
	var backends []networkingv1.IngressBackend
	if mci.Spec.DefaultBackend != nil {
		backends = append(backends, *mci.Spec.DefaultBackend)
	}

	for _, rule := range mci.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			backends = append(backends, path.Backend)
		}
	}

	svcNames := sets.NewString()
	for _, backend := range backends {
		svcNames.Insert(backend.Service.Name)
	}
	return svcNames.List()
}
