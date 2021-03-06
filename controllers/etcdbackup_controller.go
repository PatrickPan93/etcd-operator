/*
Copyright 2021 Simonpoon93.

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

package controllers

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/api/resource"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	etcdv1alpha1 "github.com/Simonpoon93/etcd-operator/api/v1alpha1"
)

// EtcdBackupReconciler reconciles a EtcdBackup object
type EtcdBackupReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// 封装一下backupState, 除了etcdbackup本身 还习总能了actual 和 desired 的信息
type backupState struct {
	backup *etcdv1alpha1.EtcdBackup
	// 这里的状态不等于status job pod在执行
	actual *backupStateContainer
	// 期望状态
	desired *backupStateContainer
}

type backupStateContainer struct {
	pod *corev1.Pod // backup.name namespace
}

// 获取真实状态
func (r *EtcdBackupReconciler) setStateActual(ctx context.Context, state *backupState) error {

	var actual backupStateContainer

	// 生成获取对象的key
	key := client.ObjectKey{
		Name:      state.backup.Name,
		Namespace: state.backup.Namespace,
	}

	// 获取对应的Pod
	actual.pod = &corev1.Pod{}
	if err := r.Get(ctx, key, actual.pod); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("getting pod error: %s", err)
		}
		// 通过上面非空错误筛选, 说明pod为空, 置为nil
		actual.pod = nil
	}
	// 填充当前真实的状态
	state.actual = &actual
	return nil
}

// 获取期望状态
func (r *EtcdBackupReconciler) setStateDesired(ctx context.Context, state *backupState) error {

	var desired backupStateContainer
	// 根据EtcdBackup信息 创建一个用于备份etcd的Pod
	pod := podForBackup(state.backup)

	// 配置Controller reference
	if err := controllerutil.SetControllerReference(state.backup, pod, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference error: %s", err)
	}
	desired.pod = pod

	// 获取到期望的对象
	state.desired = &desired

	return nil
}

// Pod构造函数
func podForBackup(backup *etcdv1alpha1.EtcdBackup) *corev1.Pod {

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				corev1.Container{
					Name:  "etcd-backup",
					Image: backup.Spec.BackupImage,
					Args: []string{
						"--etcd-endpoints", backup.Spec.Endpoints,
						// TODO 其他参数
					},
					Resources: corev1.ResourceRequirements{
						Requests: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("100Mi"),
						},
						Limits: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("500Mi"),
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// 获取当前应用的整个状态
func (r *EtcdBackupReconciler) getState(ctx context.Context, req ctrl.Request) (*backupState, error) {

	var state backupState

	// 获取EtcdBackup对象
	state.backup = &etcdv1alpha1.EtcdBackup{}
	if err := r.Get(ctx, req.NamespacedName, state.backup); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, fmt.Errorf("getting backup object error: %s", err)
		}
		state.backup = nil
		return &state, nil
	}

	// 从上面获得了EtchBackup对象(为空或存在）

	// 获取当前真实状态
	if err := r.setStateActual(ctx, &state); err != nil {
		return nil, fmt.Errorf("setting actual state error: %s", err)
	}

	// 获取期望状态
	if err := r.setStateDesired(ctx, &state); err != nil {
		return nil, fmt.Errorf("setting desired state error: %s", err)
	}
	return &state, nil
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=etcd.oschina.cn,resources=etcdbackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=etcd.oschina.cn,resources=etcdbackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=etcd.oschina.cn,resources=etcdbackups/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the EtcdBackup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *EtcdBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("etcdbackup", req.NamespacedName)
	// 获取backupState
	state, err := r.getState(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 根据状态来判断下一步需要执行的动作（state包含了crd资源对象状态 pod当前真实状态 期望状态）
	var action Action

	// 开始判断状态
	switch {
	case state.backup == nil: // 被删除
		log.Info("Backup Object not found")
	case !state.backup.DeletionTimestamp.IsZero():
		// 被标记为删除
		log.Info("Backup Object has been deleted")
	case state.backup.Status.Phase == "":
		// 开始备份,先标记状态为备份中
		log.Info("Backup starting")

		// 深拷贝当前state的状态 并且调整其状态
		newBackup := state.backup.DeepCopy()
		newBackup.Status.Phase = etcdv1alpha1.EtcdBackupPhaseBackingUp
		action = &PatchStatus{
			client:   r.Client,
			original: state.backup,
			new:      newBackup,
		}
	case state.backup.Status.Phase == etcdv1alpha1.EtcdBackupPhaseFailed:
		log.Info("Backup has failed. Ignoring...")
	case state.backup.Status.Phase == etcdv1alpha1.EtcdBackupPhaseCompleted:
		log.Info("Backup has completed. Ignoring...")
	case state.actual.pod == nil:
		// 当前还没有执行任务的pod 则创建
		log.Info("Backup Pod does not exist. Creating...")
		action = &CreateObject{
			client: r.Client,
			obj:    state.desired.pod,
		}
	case state.actual.pod.Status.Phase == corev1.PodFailed:
		log.Info("Backup Pod running failed.")
		// Pod运行失败则调整crd资源的状态
		newBackup := state.backup.DeepCopy()
		newBackup.Status.Phase = etcdv1alpha1.EtcdBackupPhaseFailed
		action = &PatchStatus{
			client:   r.Client,
			original: state.backup,
			new:      newBackup,
		}
	case state.actual.pod.Status.Phase == corev1.PodSucceeded:
		// Pod执行成功, 更新备份状态
		log.Info("Backup Pod success.")
		newBackup := state.backup.DeepCopy()
		newBackup.Status.Phase = etcdv1alpha1.EtcdBackupPhaseCompleted
		action = &PatchStatus{
			client:   r.Client,
			original: state.backup,
			new:      newBackup,
		}
	}

	if action != nil {
		if err := action.Execute(ctx); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EtcdBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&etcdv1alpha1.EtcdBackup{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
