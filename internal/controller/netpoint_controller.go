/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	routev1 "github.com/openshift/api/route/v1"

	networkingv1 "github.com/TwistedSolutions/polimorph-operator/api/v1"
)

// NetPointReconciler reconciles a NetPoint object
type NetPointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=netpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=netpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.twistedsolutions.se,resources=netpoints/finalizers,verbs=update

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete

// NetPointReconciler is a controller that reconciles NetPoint custom resources.
// It ensures that the desired state of the NetPoint resource is reflected in the cluster by managing
// associated Kubernetes resources such as Deployments, Services, ServiceAccounts, ClusterRoles,
// and ClusterRoleBindings. Additionally, it supports OpenShift-specific resources like Routes.
func (r *NetPointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the NetPoint instance
	var netpoint networkingv1.NetPoint
	if err := r.Client.Get(ctx, req.NamespacedName, &netpoint); err != nil {
		log.Error(err, "Failed to get NetPoint instance")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpoint.Name + "-sa",
			Namespace: netpoint.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		return controllerutil.SetControllerReference(&netpoint, sa, r.Scheme)
	})
	if err != nil {
		log.Error(err, "failed to create or update ServiceAccount")
		return ctrl.Result{}, err
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpoint.Name,
			Namespace: netpoint.Namespace,
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		// Prepare container security context
		secCtx := &corev1.SecurityContext{
			AllowPrivilegeEscalation: pointer.Bool(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			Privileged:   pointer.Bool(false),
			RunAsNonRoot: pointer.Bool(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}

		// If not running on OpenShift, set fixed RunAsUser and RunAsGroup.
		if !netpoint.Spec.Openshift.IsOpenshift {
			secCtx.RunAsUser = pointer.Int64(1000)
			secCtx.RunAsGroup = pointer.Int64(3000)
		}

		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &netpoint.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": netpoint.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": netpoint.Name},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: sa.Name,
					Containers: []corev1.Container{
						{
							Name:  "netpoint",
							Image: "quay.io/twistedsolutions/netpoint:" + netpoint.Spec.Version,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "GITOPS_TRACKING_REQUIREMENT",
									Value: netpoint.Spec.GitOpsTrackingRequirement,
								},
							},
							SecurityContext: secCtx,
							Resources:       netpoint.Spec.Resources,
						},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(&netpoint, deploy, r.Scheme)
	})
	if err != nil {
		log.Error(err, "failed to create or update Deployment")
		return ctrl.Result{}, err
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpoint.Name,
			Namespace: netpoint.Namespace,
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = map[string]string{"app": netpoint.Name}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			},
		}
		return controllerutil.SetControllerReference(&netpoint, svc, r.Scheme)
	})
	if err != nil {
		log.Error(err, "failed to create or update Service")
		return ctrl.Result{}, err
	}

	if netpoint.Spec.Openshift.IsOpenshift && netpoint.Spec.Openshift.OpenshiftRoute.Create {
		route := &routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Name:      netpoint.Name + "-route",
				Namespace: netpoint.Namespace,
			},
		}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
			route.Spec = routev1.RouteSpec{
				Host: netpoint.Spec.Openshift.OpenshiftRoute.Host,
				To: routev1.RouteTargetReference{
					Kind: "Service",
					Name: netpoint.Name,
				},
				Port: &routev1.RoutePort{
					TargetPort: intstr.FromInt(8080),
				},
			}
			return controllerutil.SetControllerReference(&netpoint, route, r.Scheme)
		})
		if err != nil {
			log.Error(err, "failed to create or update Route")
			return ctrl.Result{}, err
		}
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "netpoint-networkpolicy-reader",
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, cr, func() error {
		// Define the desired rules for the ClusterRole.
		cr.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch"},
			},
		}
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create or update ClusterRole")
		return ctrl.Result{}, err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: netpoint.Name + "-crb",
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		// Do not set an owner reference here because the resource is cluster-scoped.
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa.Name,
				Namespace: netpoint.Namespace,
			},
		}
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "netpoint-networkpolicy-reader",
		}
		return nil
	})
	if err != nil {
		log.Error(err, "failed to create or update ClusterRoleBinding")
		return ctrl.Result{}, err
	}

	log.Info(fmt.Sprintf("NetPoint %s reconciled", netpoint.Name))
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NetPointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.NetPoint{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.Service{}).
		Owns(&rbacv1.ClusterRole{}).
		Complete(r)
}
