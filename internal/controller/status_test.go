package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	gpuv1alpha1 "github.com/abhirammv2000/kube-op-lite/api/v1alpha1"
)

// These specs cover how status follows the child Deployment and its pods. envtest
// has no Deployment controller and no scheduler, so the specs play those roles by
// writing the Deployment status and the pod's PodScheduled condition themselves.
var _ = Describe("GpuWorkload status", func() {
	const (
		name      = "status-demo"
		namespace = "default"
		podName   = "status-demo-pod"
		timeout   = 10 * time.Second
		interval  = 250 * time.Millisecond
	)

	key := types.NamespacedName{Name: name, Namespace: namespace}

	reconcile := func() {
		r := &GpuWorkloadReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, ctrlRequest(key))
		Expect(err).NotTo(HaveOccurred())
	}

	create := func(replicas, gpus int32, priority gpuv1alpha1.Priority) {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: replicas, Image: "nginx:1.27", GpuCount: gpus, Priority: priority},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcile() // adds the finalizer and creates the children
	}

	get := func() *gpuv1alpha1.GpuWorkload {
		wl := &gpuv1alpha1.GpuWorkload{}
		Expect(k8sClient.Get(ctx, key, wl)).To(Succeed())
		return wl
	}

	// setAvailable plays the Deployment controller: it reports n pods as available.
	setAvailable := func(n int32) {
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		dep.Status.Replicas = n
		dep.Status.UpdatedReplicas = n
		dep.Status.ReadyReplicas = n
		dep.Status.AvailableReplicas = n
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
	}

	AfterEach(func() {
		// Let the drain finish: it waits for the Deployment to report zero pods.
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, key, dep); err == nil {
			dep.Status = appsv1.DeploymentStatus{}
			_ = k8sClient.Status().Update(ctx, dep)
		}
		_ = k8sClient.Delete(ctx, &gpuv1alpha1.GpuWorkload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		Eventually(func() bool {
			r := &GpuWorkloadReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, ctrlRequest(key))
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &gpuv1alpha1.GpuWorkload{}))
		}, timeout, interval).Should(BeTrue())
		_ = k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace}})
	})

	It("is Pending with Available=False before any pod is available", func() {
		create(2, 1, gpuv1alpha1.PriorityMedium)

		wl := get()
		Expect(wl.Status.Phase).To(Equal(gpuv1alpha1.PhasePending))
		Expect(wl.Status.AvailableReplicas).To(BeZero())
		available := meta.FindStatusCondition(wl.Status.Conditions, condAvailable)
		Expect(available).NotTo(BeNil())
		Expect(available.Status).To(Equal(metav1.ConditionFalse))
	})

	It("becomes Running once every replica is available", func() {
		create(2, 1, gpuv1alpha1.PriorityMedium)

		setAvailable(2)
		reconcile()

		wl := get()
		Expect(wl.Status.Phase).To(Equal(gpuv1alpha1.PhaseRunning))
		Expect(wl.Status.AvailableReplicas).To(Equal(int32(2)))
		Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, condAvailable)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, condUnschedulable)).To(BeFalse())
	})

	It("is Provisioning when it falls back to partially available after running", func() {
		create(2, 1, gpuv1alpha1.PriorityMedium)
		setAvailable(2)
		reconcile()
		Expect(get().Status.Phase).To(Equal(gpuv1alpha1.PhaseRunning))

		setAvailable(1)
		reconcile()

		wl := get()
		Expect(wl.Status.Phase).To(Equal(gpuv1alpha1.PhaseProvisioning))
		Expect(wl.Status.AvailableReplicas).To(Equal(int32(1)))
	})

	It("reports GpuUnschedulable with the scheduler's message when a pod cannot be placed", func() {
		create(1, 2, gpuv1alpha1.PriorityLow)

		const message = "0/1 nodes are available: 1 Insufficient nvidia.com/gpu."
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
				Labels:    map[string]string{labelWorkload: name},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workload", Image: "nginx:1.27"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		pod.Status.Conditions = []corev1.PodCondition{{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionFalse,
			Reason:             corev1.PodReasonUnschedulable,
			Message:            message,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcile()

		wl := get()
		Expect(wl.Status.Phase).To(Equal(gpuv1alpha1.PhasePending))
		cond := meta.FindStatusCondition(wl.Status.Conditions, condUnschedulable)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("InsufficientGPU"))
		Expect(cond.Message).To(Equal(message))
	})

	It("clears GpuUnschedulable once the pod is placed", func() {
		create(1, 1, gpuv1alpha1.PriorityLow)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace, Labels: map[string]string{labelWorkload: name}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "workload", Image: "nginx:1.27"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		pod.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "no GPUs", LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		reconcile()
		Expect(meta.IsStatusConditionTrue(get().Status.Conditions, condUnschedulable)).To(BeTrue())

		pod.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		setAvailable(1)
		reconcile()

		wl := get()
		Expect(wl.Status.Phase).To(Equal(gpuv1alpha1.PhaseRunning))
		Expect(meta.IsStatusConditionTrue(wl.Status.Conditions, condUnschedulable)).To(BeFalse())
	})

	It("moves the pods to the new PriorityClass when the priority changes", func() {
		create(1, 1, gpuv1alpha1.PriorityLow)

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(dep.Spec.Template.Spec.PriorityClassName).To(Equal("kube-op-lite-low"))

		wl := get()
		wl.Spec.Priority = gpuv1alpha1.PriorityHigh
		Expect(k8sClient.Update(ctx, wl)).To(Succeed())
		reconcile()

		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(dep.Spec.Template.Spec.PriorityClassName).To(Equal("kube-op-lite-high"))
		Expect(dep.Spec.Template.Annotations).To(HaveKeyWithValue(annPriority, "high"))

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kube-op-lite-high"}, pc)).To(Succeed())
		Expect(pc.Value).To(Equal(int32(100000)))
	})

	It("rolls GPU workloads out one pod at a time without surging", func() {
		create(2, 2, gpuv1alpha1.PriorityMedium)

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		ru := dep.Spec.Strategy.RollingUpdate
		Expect(ru).NotTo(BeNil())
		Expect(ru.MaxSurge.IntValue()).To(Equal(0))
		Expect(ru.MaxUnavailable.IntValue()).To(Equal(1))
	})

	It("keeps the Kubernetes default rollout for workloads without GPUs", func() {
		create(2, 0, gpuv1alpha1.PriorityMedium)

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		ru := dep.Spec.Strategy.RollingUpdate
		Expect(ru).NotTo(BeNil())
		Expect(ru.MaxSurge.String()).To(Equal("25%"))
		Expect(ru.MaxUnavailable.String()).To(Equal("25%"))
	})

	It("requests no GPU resource when gpuCount is zero", func() {
		create(1, 0, gpuv1alpha1.PriorityMedium)

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(dep.Spec.Template.Spec.Containers[0].Resources.Limits).NotTo(HaveKey(gpuResource))
	})
})
