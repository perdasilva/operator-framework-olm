package olm_auto_labeler

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type OLMAutoLabelerReconciler struct {
	client.Client
	logger logr.Logger
}

type OLMAutoLabellerReconcilerOption func(reconciler OLMAutoLabelerReconciler)

func WithLogger(logger logr.Logger) OLMAutoLabellerReconcilerOption {
	return func(reconciler OLMAutoLabelerReconciler) {
		reconciler.logger = logger
	}
}

func NewOLMAutoLabelerReconciler(client client.Client, options ...OLMAutoLabellerReconcilerOption) *OLMAutoLabelerReconciler {
	reconciler := OLMAutoLabelerReconciler{
		Client: client,
	}
	for _, option := range options {
		option(reconciler)
	}

	return &reconciler
}

func (a *OLMAutoLabelerReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {

	//rs := &appsv1.ReplicaSet{}
	//err := a.Get(ctx, req.NamespacedName, rs)
	//if err != nil {
	//	return reconcile.Result{}, err
	//}
	//
	//pods := &corev1.PodList{}
	//err = a.List(ctx, pods, client.InNamespace(req.Namespace), client.MatchingLabels(rs.Spec.Template.Labels))
	//if err != nil {
	//	return reconcile.Result{}, err
	//}
	//
	//rs.Labels["pod-count"] = fmt.Sprintf("%v", len(pods.Items))
	//err = a.Update(ctx, rs)
	//if err != nil {
	//	return reconcile.Result{}, err
	//}
	a.logger.Info("Got a request: %s", req.NamespacedName)

	return reconcile.Result{}, nil
}
