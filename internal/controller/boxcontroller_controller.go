/*
Copyright 2026.

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
	"sort"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	apicorev1 "github.com/zhengzhihua2017/box-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// BoxControllerReconciler reconciles a BoxController object
type BoxControllerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core.zhengzhihua2017.com,resources=boxcontrollers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.zhengzhihua2017.com,resources=boxcontrollers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.zhengzhihua2017.com,resources=boxcontrollers/finalizers,verbs=update

// Reconcile 主流程
func (r *BoxControllerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 步骤1：获取 BoxController CR
	boxCtrl, err := r.getBoxController(ctx, req)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 步骤2：查询所有 app=box 的 Pod
	pods, err := r.listBoxPods(ctx, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 步骤3：收集所有 Pod 的 id 信息
	usedIDs, podNameToID := collectPodIDs(pods)

	// 步骤4：更新 CR status
	r.updateBoxControllerStatus(ctx, boxCtrl, podNameToID, log)

	// 步骤5：计算未分配的最小 id
	n := boxCtrl.Spec.Replicas
	freeIDs := []int{}
	for i := 1; i <= n; i++ {
		if !usedIDs[i] {
			freeIDs = append(freeIDs, i)
		}
	}
	sort.Ints(freeIDs)

	// 步骤6：副本数变化时自动增删 Pod
	r.syncPodsWithReplicas(ctx, pods, n, freeIDs, podNameToID, req.Namespace, log)

	// 每隔1分钟自动重试
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// 获取 BoxController CR 对象
func (r *BoxControllerReconciler) getBoxController(ctx context.Context, req ctrl.Request) (*apicorev1.BoxController, error) {
	var boxCtrl apicorev1.BoxController
	if err := r.Get(ctx, req.NamespacedName, &boxCtrl); err != nil {
		return nil, err
	}
	return &boxCtrl, nil
}

// 查询所有 app=box 的 Pod
func (r *BoxControllerReconciler) listBoxPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabels{"app": "box"}); err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// 收集所有 Pod 的 id 信息，返回已用 id、podName->id 映射
func collectPodIDs(pods []corev1.Pod) (map[int]bool, map[string]int) {
	usedIDs := map[int]bool{}
	podNameToID := map[string]int{}
	for _, pod := range pods {
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "id" {
				if id, err := strconv.Atoi(env.Value); err == nil {
					usedIDs[id] = true
					podNameToID[pod.Name] = id
				}
			}
		}
	}
	return usedIDs, podNameToID
}

// 更新 CR status，记录每个 Pod 的 name 和 id
func (r *BoxControllerReconciler) updateBoxControllerStatus(ctx context.Context, boxCtrl *apicorev1.BoxController, podNameToID map[string]int, log logr.Logger) {
	statusPods := make([]apicorev1.PodIDStatus, 0, len(podNameToID))
	for name, id := range podNameToID {
		statusPods = append(statusPods, apicorev1.PodIDStatus{Name: name, ID: id})
	}
	boxCtrl.Status.Pods = statusPods
	if err := r.Status().Update(ctx, boxCtrl); err != nil {
		log.Error(err, "update status failed")
	}
}

// 根据副本数自动增删 Pod，保证实际副本数与期望一致
func (r *BoxControllerReconciler) syncPodsWithReplicas(
	ctx context.Context,
	pods []corev1.Pod,
	replicas int,
	freeIDs []int,
	podNameToID map[string]int,
	namespace string,
	log logr.Logger,
) {
	// 少于期望副本数，循环创建新 Pod
	if len(pods) < replicas && len(freeIDs) > 0 {
		need := replicas - len(pods)
		for i := 0; i < need && i < len(freeIDs); i++ {
			id := freeIDs[i]
			newPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("box-%d", id),
					Namespace: namespace,
					Labels:    map[string]string{"app": "box"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "box",
						Image: "alpine:latest",
						Env: []corev1.EnvVar{{
							Name:  "id",
							Value: strconv.Itoa(id),
						}},
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{"echo \"id=$id\" && sleep 3600"},
					}},
				},
			}
			if err := r.Create(ctx, newPod); err != nil {
				log.Error(err, "failed to create pod")
			} else {
				log.Info("Created new pod", "id", id)
			}
		}
	}

	// 多于期望副本数，自动删除多余的 Pod
	if len(pods) > replicas {
		podsToDelete := make([]struct {
			Name string
			ID   int
		}, 0, len(podNameToID))
		for name, id := range podNameToID {
			podsToDelete = append(podsToDelete, struct {
				Name string
				ID   int
			}{Name: name, ID: id})
		}
		sort.Slice(podsToDelete, func(i, j int) bool {
			return podsToDelete[i].ID > podsToDelete[j].ID // id 大的排前面
		})
		delCount := len(pods) - replicas
		for i := 0; i < delCount && i < len(podsToDelete); i++ {
			podName := podsToDelete[i].Name
			if err := r.Delete(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: namespace,
				},
			}); err != nil {
				log.Error(err, "failed to delete pod", "pod", podName)
			} else {
				log.Info("Deleted pod to match replicas", "pod", podName)
			}
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *BoxControllerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apicorev1.BoxController{}).
		Named("boxcontroller").
		Complete(r)
}
