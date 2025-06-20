/*
Copyright 2023 kubecombo.

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
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	myv1 "github.com/kubecombo/kube-combo/api/v1"
)

const (
	EnablePingerLabel = "kubecombo.com/enable-pinger"
	DebuggerName      = "debugger"
	PingerName        = "pinger"

	DebuggerStartCMD = "/kubeovn/debugger-start.sh"
	PingerStartCMD   = "/kubeovn/pinger-start.sh"

	// WorkloadTypeDeployment is the workload type for deployment
	WorkloadTypeDeployment = "deployment"
	// WorkloadTypeDaemonset is the workload type for daemonset
	WorkloadTypeDaemonset = "daemonset"
	// debugger env
	Subnet = "SUBNET"
	// pinger env
	MustReach    = "MUST_REACH"
	Interval     = "INTERVAL"
	EnableMetric = "ENABLE_METRIC"
	Arpping      = "ARPING"
	Ping         = "PING"
	TcpPing      = "TCP_PING"
	UdpPing      = "UDP_PING"
	Dns          = "DNS"
)

// DebuggerReconciler reconciles a Debugger object
type DebuggerReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	KubeClient kubernetes.Interface
	RestConfig *rest.Config
	Log        logr.Logger
	Namespace  string
	Reload     chan event.GenericEvent
}

// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=debuggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=debuggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=debuggers/finalizers,verbs=update
// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=pingers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=pingers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vpn-gw.kubecombo.com,resources=pingers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/scale,verbs=get;watch;update
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets/scale,verbs=get;watch;update
// +kubebuilder:rbac:groups=apps,resources=daemonsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Debugger object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *DebuggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// TODO(user): your logic here
	// delete debugger itself, its owned deploy will be deleted automatically
	namespacedName := req.NamespacedName.String()
	r.Log.Info("start reconcile", "debugger", namespacedName)
	defer r.Log.Info("end reconcile", "debugger", namespacedName)
	updates.Inc()
	res, err := r.handleAddOrUpdateDebugger(ctx, req)
	switch res {
	case SyncStateError:
		updateErrors.Inc()
		r.Log.Error(err, "failed to handle debugger, will retry")
		return ctrl.Result{RequeueAfter: 3 * time.Second}, errRetry
	case SyncStateErrorNoRetry:
		updateErrors.Inc()
		r.Log.Error(err, "failed to handle debugger, not retry")
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DebuggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&myv1.Debugger{},
			builder.WithPredicates(
				predicate.NewPredicateFuncs(
					func(object client.Object) bool {
						_, ok := object.(*myv1.Debugger)
						if !ok {
							err := errors.New("invalid debugger")
							r.Log.Error(err, "expected debugger in workqueue but got something else")
							return false
						}
						return true
					},
				),
			),
		).
		Owns(&appsv1.DaemonSet{}).  // for all node pod case
		Owns(&appsv1.Deployment{}). // for single pod case
		Owns(&myv1.Pinger{}).
		Complete(r)
}

func (r *DebuggerReconciler) handleAddOrUpdateDebugger(ctx context.Context, req ctrl.Request) (SyncState, error) {
	// Implement the logic to handle the addition or update of a Debugger resource
	// create debugger daemonset or deployment
	namespacedName := req.NamespacedName.String()
	r.Log.Info("start handleAddOrUpdateDebugger", "debugger", namespacedName)
	defer r.Log.Info("end handleAddOrUpdateDebugger", "debugger", namespacedName)

	// fetch debugger
	debugger, err := r.getDebugger(ctx, req.NamespacedName)
	if err != nil {
		r.Log.Error(err, "failed to get debugger")
		return SyncStateErrorNoRetry, err
	}
	if debugger == nil {
		// debugger deleted
		return SyncStateSuccess, nil
	}
	if err := r.validateDebugger(debugger); err != nil {
		r.Log.Error(err, "failed to validate debugger")
		// invalid spec, no retry
		return SyncStateErrorNoRetry, err
	}

	var pinger *myv1.Pinger
	if debugger.Spec.Pinger != "" {
		pinger = &myv1.Pinger{
			ObjectMeta: metav1.ObjectMeta{
				Name:      debugger.Spec.Pinger,
				Namespace: debugger.Namespace,
			},
		}
		pinger, err = r.getPinger(ctx, pinger)
		if err != nil {
			r.Log.Error(err, "failed to get pinger")
			return SyncStateError, err
		}
		if err := r.validatePinger(pinger, debugger.Spec.EnablePinger); err != nil {
			r.Log.Error(err, "failed to validate pinger")
			// invalid spec no retry
			return SyncStateErrorNoRetry, err
		}
	}
	// create debugger or update
	if debugger.Spec.WorkloadType == WorkloadTypeDeployment {
		// deployment for one pod case
		if err := r.handleAddOrUpdateDeploy(req, debugger, pinger); err != nil {
			r.Log.Error(err, "failed to handleAddOrUpdateDeploy")
			return SyncStateError, err
		}
	} else {
		// daemonset for all node case
		if err := r.handleAddOrUpdateDaemonset(req, debugger, pinger); err != nil {
			r.Log.Error(err, "failed to handleAddOrUpdateDaemonset")
			return SyncStateError, err
		}
	}
	return SyncStateSuccess, nil
}

func (r *DebuggerReconciler) getDebugger(ctx context.Context, name types.NamespacedName) (*myv1.Debugger, error) {
	var res myv1.Debugger
	err := r.Get(ctx, name, &res)
	if apierrors.IsNotFound(err) {
		// in case of delete
		return nil, nil
	}
	if err != nil {
		r.Log.Error(err, "failed to get debugger")
		return nil, err
	}
	return &res, nil
}

func (r *DebuggerReconciler) getPinger(ctx context.Context, pinger *myv1.Pinger) (*myv1.Pinger, error) {
	var res myv1.Pinger
	name := types.NamespacedName{
		Name:      pinger.Name,
		Namespace: pinger.Namespace,
	}
	err := r.Get(ctx, name, &res)
	if err != nil {
		r.Log.Error(err, "failed to get pinger")
		return nil, err
	}

	return &res, nil
}

func (r *DebuggerReconciler) validateDebugger(debugger *myv1.Debugger) error {
	r.Log.V(3).Info("start validateDebugger", "debugger", debugger)
	if debugger.Spec.CPU == "" {
		err := errors.New("debugger pod cpu is required")
		r.Log.Error(err, "should set cpu")
		return err
	}
	if debugger.Spec.Memory == "" {
		err := errors.New("debugger pod memory is required")
		r.Log.Error(err, "should set memory")
		return err
	}
	if debugger.Spec.Image == "" {
		err := fmt.Errorf("debugger %s image is required", debugger.Name)
		r.Log.Error(err, "should set image")
		return err
	}

	if debugger.Spec.WorkloadType == "" {
		err := errors.New("debugger workload type is required")
		r.Log.Error(err, "should set workload type")
		return err
	}
	if debugger.Spec.WorkloadType != "daemonset" && debugger.Spec.WorkloadType != "deployment" {
		err := fmt.Errorf("debugger %s workload type is invalid, should be daemonset or deployment", debugger.Name)
		r.Log.Error(err, "should set valid workload type")
		return err
	}

	if debugger.Spec.WorkloadType == WorkloadTypeDeployment {
		if debugger.Spec.Replicas < 1 {
			err := fmt.Errorf("debugger %s replicas should be at least 1", debugger.Name)
			r.Log.Error(err, "should set valid replicas")
			return err
		}
	}
	if debugger.Spec.WorkloadType == WorkloadTypeDaemonset {
		if debugger.Spec.NodeName != "" {
			err := fmt.Errorf("debugger %s daemonset not need node name", debugger.Name)
			r.Log.Error(err, "should not set node name for daemonset debugger pod")
			return err
		}
	}

	if debugger.Spec.EnablePinger && debugger.Spec.Pinger == "" {
		err := fmt.Errorf("debugger %s enable pinger, but pinger is empty", debugger.Name)
		r.Log.Error(err, "should set pinger info")
		return err
	}

	return nil
}

func (r *DebuggerReconciler) validatePinger(pinger *myv1.Pinger, enablePinger bool) error {
	// 1. debugger has no pinger container, sleep infinite
	if !enablePinger {
		r.Log.Info("pinger is not enabled, skip validation", "pinger", pinger.Name)
		return nil
	}
	// 2. debugger has pinger container, check pinger spec
	if pinger.DeletionTimestamp != nil {
		// pinger is being deleted
		r.Log.Info("pinger is being deleted, skip validation", "pinger", pinger.Name)
		return nil
	}

	if pinger.Spec.Image == "" {
		err := fmt.Errorf("pinger %s image is required", pinger.Name)
		r.Log.Error(err, "should set pinger image")
		return err
	}

	if pinger.Spec.Interval <= 0 {
		err := fmt.Errorf("pinger %s interval should be greater than 0", pinger.Name)
		r.Log.Error(err, "should set pinger interval")
		return err
	}

	if pinger.Spec.Ping == "" &&
		pinger.Spec.TcpPing == "" &&
		pinger.Spec.UdpPing == "" &&
		pinger.Spec.Arpping == "" &&
		pinger.Spec.Dns == "" {
		err := fmt.Errorf("pinger %s must set at least one kind of ping target", pinger.Name)
		r.Log.Error(err, "should set ping task")
		return err
	}

	return nil
}

func (r *DebuggerReconciler) handleAddOrUpdateDeploy(req ctrl.Request, debugger *myv1.Debugger, pinger *myv1.Pinger) error {
	// create or update deployment
	needToCreate := false
	oldDeploy := &appsv1.Deployment{}
	err := r.Get(context.Background(), req.NamespacedName, oldDeploy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			needToCreate = true
		} else {
			r.Log.Error(err, "failed to get deployment")
			return err
		}
	}
	newDebugger := debugger.DeepCopy()
	// create
	if needToCreate {
		// create deployment
		newDeploy := r.getDebuggerDeploy(debugger, pinger, nil)
		err = r.Create(context.Background(), newDeploy)
		if err != nil {
			r.Log.Error(err, "failed to create the new deployment")
			return err
		}
		time.Sleep(5 * time.Second)
		return nil
	}
	// update
	if r.isChanged(newDebugger) {
		// update deployment
		newDeploy := r.getDebuggerDeploy(debugger, pinger, oldDeploy.DeepCopy())
		err = r.Update(context.Background(), newDeploy)
		if err != nil {
			r.Log.Error(err, "failed to update the deployment")
			return err
		}
		time.Sleep(5 * time.Second)
		return nil
	}
	// no change
	r.Log.Info("debugger deployment not changed", "debugger", debugger.Name)
	return nil
}

func (r *DebuggerReconciler) handleAddOrUpdateDaemonset(req ctrl.Request, debugger *myv1.Debugger, pinger *myv1.Pinger) error {
	// create or update daemonset
	needToCreate := false
	oldDs := &appsv1.DaemonSet{}
	err := r.Get(context.Background(), req.NamespacedName, oldDs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			needToCreate = true
		} else {
			r.Log.Error(err, "failed to get daemonset")
			return err
		}
	}
	newDebugger := debugger.DeepCopy()
	// create
	if needToCreate {
		// create daemonset
		newDs := r.getDebuggerDaemonset(debugger, pinger, nil)
		err = r.Create(context.Background(), newDs)
		if err != nil {
			r.Log.Error(err, "failed to create the new daemonset")
			return err
		}
		time.Sleep(5 * time.Second)
		return nil
	}
	// update
	if r.isChanged(newDebugger) {
		// update daemonset
		newDs := r.getDebuggerDaemonset(debugger, pinger, oldDs.DeepCopy())
		err = r.Update(context.Background(), newDs)
		if err != nil {
			r.Log.Error(err, "failed to update the daemonset")
			return err
		}
		time.Sleep(5 * time.Second)
		return nil
	}
	// no change
	r.Log.Info("debugger daemonset not changed", "debugger", debugger.Name)
	return nil
}

func labelsFor(debugger *myv1.Debugger) map[string]string {
	return map[string]string{
		EnablePingerLabel: strconv.FormatBool(debugger.Spec.EnablePinger),
		DebuggerName:      debugger.Name,
	}
}

func (r *DebuggerReconciler) getDebuggerDeploy(debugger *myv1.Debugger, pinger *myv1.Pinger, oldDeploy *appsv1.Deployment) (newDeploy *appsv1.Deployment) {
	namespacedName := fmt.Sprintf("%s/%s", debugger.Namespace, debugger.Name)
	r.Log.Info("start deployForDebugger", "debugger", namespacedName)
	defer r.Log.Info("end deployForDebugger", "debugger", namespacedName)

	replicas := debugger.Spec.Replicas
	labels := labelsFor(debugger)
	newPodAnnotations := map[string]string{}
	if oldDeploy != nil && len(oldDeploy.Annotations) != 0 {
		newPodAnnotations = oldDeploy.Annotations
	}
	podAnnotations := map[string]string{
		KubeovnLogicalSwitchAnnotation: debugger.Spec.Subnet,
		KubeovnIngressRateAnnotation:   debugger.Spec.QoSBandwidth,
		KubeovnEgressRateAnnotation:    debugger.Spec.QoSBandwidth,
	}
	for key, value := range podAnnotations {
		newPodAnnotations[key] = value
	}

	containers := []corev1.Container{}
	volumes := []corev1.Volume{}

	// debugger container
	debuggerContainer := r.getDebuggerContainer(debugger)
	containers = append(containers, debuggerContainer)
	if debugger.Spec.EnablePinger {
		// pinger container
		pingerContainer := r.getPingerContainer(pinger)
		containers = append(containers, pingerContainer)
	}

	newDeploy = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      debugger.Name,
			Namespace: debugger.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: newPodAnnotations,
				},
				Spec: corev1.PodSpec{
					NodeName:   debugger.Spec.NodeName,
					Containers: containers,
					Volumes:    volumes,
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			},
		},
	}
	if len(debugger.Spec.Selector) > 0 {
		selectors := make(map[string]string)
		for _, v := range debugger.Spec.Selector {
			parts := strings.Split(strings.TrimSpace(v), ":")
			if len(parts) != 2 {
				continue
			}
			selectors[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
		newDeploy.Spec.Template.Spec.NodeSelector = selectors
	}

	if len(debugger.Spec.Tolerations) > 0 {
		newDeploy.Spec.Template.Spec.Tolerations = debugger.Spec.Tolerations
	}

	if debugger.Spec.Affinity.NodeAffinity != nil ||
		debugger.Spec.Affinity.PodAffinity != nil ||
		debugger.Spec.Affinity.PodAntiAffinity != nil {
		newDeploy.Spec.Template.Spec.Affinity = &debugger.Spec.Affinity
	}

	// set owner reference
	if err := controllerutil.SetControllerReference(debugger, newDeploy, r.Scheme); err != nil {
		r.Log.Error(err, "failed to set debugger as the owner of the deployment")
		return nil
	}
	return
}

func (r *DebuggerReconciler) getDebuggerContainer(debugger *myv1.Debugger) corev1.Container {
	allowPrivilegeEscalation := true
	privileged := true

	debuggerContainer := corev1.Container{
		Name:  DebuggerName,
		Image: debugger.Spec.Image,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(debugger.Spec.CPU),
				corev1.ResourceMemory: resource.MustParse(debugger.Spec.Memory),
			},
		},
		Command: []string{DebuggerStartCMD},
		Env: []corev1.EnvVar{
			{
				Name:  Subnet,
				Value: debugger.Spec.Subnet,
			},
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			Privileged:               &privileged,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		},
	}
	return debuggerContainer
}

func (r *DebuggerReconciler) getPingerContainer(pinger *myv1.Pinger) corev1.Container {
	allowPrivilegeEscalation := true
	privileged := true

	pingerContainer := corev1.Container{
		Name:  PingerName,
		Image: pinger.Spec.Image,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(pinger.Spec.CPU),
				corev1.ResourceMemory: resource.MustParse(pinger.Spec.Memory),
			},
		},
		Command: []string{PingerStartCMD},
		Env: []corev1.EnvVar{
			{
				Name:  MustReach,
				Value: strconv.FormatBool(pinger.Spec.MustReach),
			},
			{
				Name:  Interval,
				Value: strconv.Itoa(pinger.Status.Interval),
			},
			{
				Name:  EnableMetric,
				Value: strconv.FormatBool(pinger.Spec.EnableMetric),
			},
			{
				Name:  Arpping,
				Value: pinger.Spec.Arpping,
			},
			{
				Name:  Ping,
				Value: pinger.Spec.Ping,
			},
			{
				Name:  TcpPing,
				Value: pinger.Spec.TcpPing,
			},
			{
				Name:  UdpPing,
				Value: pinger.Spec.UdpPing,
			},
			{
				Name:  Dns,
				Value: pinger.Spec.Dns,
			},
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			Privileged:               &privileged,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		},
	}
	return pingerContainer
}
func (r *DebuggerReconciler) getDebuggerDaemonset(debugger *myv1.Debugger, pinger *myv1.Pinger, oldDs *appsv1.DaemonSet) (newDs *appsv1.DaemonSet) {
	namespacedName := fmt.Sprintf("%s/%s", debugger.Namespace, debugger.Name)
	r.Log.Info("start daemonsetForDebugger", "debugger", namespacedName)
	defer r.Log.Info("end daemonsetForDebugger", "debugger", namespacedName)

	labels := labelsFor(debugger)
	newPodAnnotations := map[string]string{}
	if oldDs != nil && len(oldDs.Annotations) != 0 {
		newPodAnnotations = oldDs.Annotations
	}
	podAnnotations := map[string]string{
		KubeovnLogicalSwitchAnnotation: debugger.Spec.Subnet,
		KubeovnIngressRateAnnotation:   debugger.Spec.QoSBandwidth,
		KubeovnEgressRateAnnotation:    debugger.Spec.QoSBandwidth,
	}
	for key, value := range podAnnotations {
		newPodAnnotations[key] = value
	}

	containers := []corev1.Container{}
	volumes := []corev1.Volume{}

	// debugger container
	debuggerContainer := r.getDebuggerContainer(debugger)
	containers = append(containers, debuggerContainer)
	if debugger.Spec.EnablePinger {
		// pinger container
		pingerContainer := r.getPingerContainer(pinger)
		containers = append(containers, pingerContainer)
	}

	newDs = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      debugger.Name,
			Namespace: debugger.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: newPodAnnotations,
				},
				Spec: corev1.PodSpec{
					Containers: containers,
					Volumes:    volumes,
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &intstr.IntOrString{
						IntVal: 1, // allow one pod unavailable during update
						StrVal: "1",
					},
				},
			},
		},
	}

	if len(debugger.Spec.Selector) > 0 {
		selectors := make(map[string]string)
		for _, v := range debugger.Spec.Selector {
			parts := strings.Split(strings.TrimSpace(v), ":")
			if len(parts) != 2 {
				continue
			}
			selectors[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
		newDs.Spec.Template.Spec.NodeSelector = selectors
	}

	if len(debugger.Spec.Tolerations) > 0 {
		newDs.Spec.Template.Spec.Tolerations = debugger.Spec.Tolerations
	}

	if debugger.Spec.Affinity.NodeAffinity != nil ||
		debugger.Spec.Affinity.PodAffinity != nil ||
		debugger.Spec.Affinity.PodAntiAffinity != nil {
		newDs.Spec.Template.Spec.Affinity = &debugger.Spec.Affinity
	}

	// set owner reference
	if err := controllerutil.SetControllerReference(debugger, newDs, r.Scheme); err != nil {
		r.Log.Error(err, "failed to set debugger as the owner of the daemonset")
		// if we cannot set owner reference, we cannot manage this daemonset
		// so we return nil to skip this daemonset
		return nil
	}
	return newDs
}

func (r *DebuggerReconciler) isChanged(debugger *myv1.Debugger) bool {
	if debugger == nil {
		return false
	}
	if debugger.Spec.CPU != debugger.Status.CPU ||
		debugger.Spec.Memory != debugger.Status.Memory ||
		debugger.Spec.Image != debugger.Status.Image ||
		debugger.Spec.Replicas != debugger.Status.Replicas ||
		debugger.Spec.QoSBandwidth != debugger.Status.QoSBandwidth ||
		debugger.Spec.WorkloadType != debugger.Status.WorkloadType ||
		debugger.Spec.EnablePinger != debugger.Status.EnablePinger ||
		debugger.Spec.Pinger != debugger.Status.Pinger {
		return true
	}
	if !reflect.DeepEqual(debugger.Spec.Tolerations, debugger.Status.Tolerations) {
		return true
	}
	if !reflect.DeepEqual(debugger.Spec.Affinity, debugger.Status.Affinity) {
		return true
	}
	if debugger.Spec.NodeName != debugger.Status.NodeName {
		return true
	}
	return false
}
