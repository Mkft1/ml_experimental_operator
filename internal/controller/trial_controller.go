// internal/controller/trial_controller.go — контроллер Trial:
// один Trial = один batchv1.Job с конкретным набором гиперпараметров.
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

const (
	// trialFinalizer — защищает Trial от удаления до очистки принадлежащего ему Job.
	trialFinalizer = "trial.ml.example.com/job-cleanup"

	// requeueAfterTrialJob — периодическая проверка статуса созданного Job.
	requeueAfterTrialJob = 30 * time.Second
)

// TrialReconciler reconciles a Trial object.
type TrialReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=ml.example.com,resources=trials,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ml.example.com,resources=trials/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ml.example.com,resources=trials/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile исполняет цикл согласования Trial (см. требования Шага 4).
func (r *TrialReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("trial", req.NamespacedName)

	// 1. Получить Trial CR.
	var trial mlv1.Trial
	if err := r.Get(ctx, req.NamespacedName, &trial); err != nil {
		if apierrors.IsNotFound(err) {
			// Объект удалён из кластера — reconcile завершён, это не ошибка.
			logger.V(1).Info("Trial not found, likely deleted; nothing to do")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Trial")
		return ctrl.Result{}, err
	}

	// Метрика: считаем каждый успешный проход Reconcile по текущей фазе.
	trialReconcileTotal.WithLabelValues(string(trial.Status.Phase)).Inc()

	// 1.5 Наследуем образ/команду/ресурсы из родительского Experiment, если в Trial они не заданы.
	if err := r.inheritFromExperiment(ctx, logger, &trial); err != nil {
		return ctrl.Result{}, err
	}

	// 2. Удаление: обрабатываем finalizer — удаляем Job и снимаем finalizer.
	if !trial.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, logger, &trial)
	}

	// 3. Если finalizer отсутствует — добавить и перезапросить.
	if !controllerutil.ContainsFinalizer(&trial, trialFinalizer) {
		patched := trial.DeepCopy()
		controllerutil.AddFinalizer(patched, trialFinalizer)
		if err := r.Patch(ctx, patched, client.MergeFrom(&trial)); err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Проверяем, существует ли Job с именем trial.Name.
	jobKey := client.ObjectKey{Namespace: trial.Namespace, Name: trial.Name}
	var job batchv1.Job
	err := r.Get(ctx, jobKey, &job)
	switch {
	case apierrors.IsNotFound(err):
		// 5. Job нет — создать (image, command, env из гиперпараметров, resources, ownerRef).
		if err := r.createJobForTrial(ctx, logger, &trial); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&trial, corev1.EventTypeNormal, "CreatedJob",
			"Created training Job %q for experiment %q", jobKey.Name, trial.Spec.ExperimentRef)
		return ctrl.Result{RequeueAfter: requeueAfterTrialJob}, nil
	case err != nil:
		logger.Error(err, "failed to get Job for Trial")
		return ctrl.Result{}, err
	}

	// 6. Job существует — синхронизируем Trial.Status.Phase со статусом Job.
	return r.syncStatusFromJob(ctx, logger, &trial, &job)
}

// inheritFromExperiment подтягивает Image/Command/Resources из родительского Experiment,
// если они не заданы явно в Trial (в CRD Trial.Image помечен Required, поэтому обычно
// Experiment-контроллер заполняет эти поля сам; здесь — защита от неполных CR).
func (r *TrialReconciler) inheritFromExperiment(ctx context.Context, logger logr.Logger, trial *mlv1.Trial) error {
	if trial.Spec.Image != "" && len(trial.Spec.Command) > 0 && trial.Spec.Resources != nil {
		return nil // ничего наследовать не нужно
	}
	expKey := client.ObjectKey{Namespace: trial.Namespace, Name: trial.Spec.ExperimentRef}
	var exp mlv1.Experiment
	if err := r.Get(ctx, expKey, &exp); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("parent Experiment not found; continuing with Trial spec as-is",
				"experiment", expKey.Name)
			return nil
		}
		return err
	}

	changed := false
	patched := trial.DeepCopy()
	if patched.Spec.Image == "" && exp.Spec.Image != "" {
		patched.Spec.Image = exp.Spec.Image
		changed = true
	}
	if len(patched.Spec.Command) == 0 && len(exp.Spec.Command) > 0 {
		patched.Spec.Command = exp.Spec.Command
		changed = true
	}
	if patched.Spec.Resources == nil {
		res := exp.Spec.Resources.DeepCopy()
		patched.Spec.Resources = res
		changed = true
	}
	if changed {
		if err := r.Patch(ctx, patched, client.MergeFrom(trial)); err != nil {
			logger.Error(err, "failed to patch inherited Trial spec")
			return err
		}
		*trial = *patched
	}
	return nil
}

// createJobForTrial строит и создаёт batchv1.Job для одного Trial.
func (r *TrialReconciler) createJobForTrial(ctx context.Context, logger logr.Logger, trial *mlv1.Trial) error {
	backoffLimit := int32(0) // ретраями управляет оператор (spec.attempt), не Job
	ttl := int32(60 * 60)    // GC объекта Job через час после завершения

	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}
	if res := trial.Spec.Resources; res != nil {
		if !res.CPU.IsZero() {
			resources.Limits[corev1.ResourceCPU] = res.CPU
			resources.Requests[corev1.ResourceCPU] = res.CPU
		}
		if !res.Memory.IsZero() {
			resources.Limits[corev1.ResourceMemory] = res.Memory
			resources.Requests[corev1.ResourceMemory] = res.Memory
		}
		if res.GPU != nil && !res.GPU.IsZero() {
			// NVIDIA GPU как extended resource (целое число).
			gpu := *res.GPU
			resources.Limits["nvidia.com/gpu"] = gpu
		}
	}

	container := corev1.Container{
		Name:      "trainer",
		Image:     trial.Spec.Image,
		Command:   trial.Spec.Command,
		Resources: resources,
		Env:       hyperparamEnv(trial.Spec.Hyperparameters),
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        trial.Name,
			Namespace:   trial.Namespace,
			Labels:      trialLabels(trial),
			Annotations: map[string]string{"ml.example.com/attempt": fmt.Sprint(maxInt32(trial.Spec.Attempt, 1))},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Completions:             ptrInt32(1),
			Parallelism:             ptrInt32(1),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: trialLabels(trial)},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{container},
				},
			},
		},
	}

	// OwnerReference на Trial — garbage collection Job вместе с Trial.
	if err := ctrl.SetControllerReference(trial, job, r.Scheme); err != nil {
		logger.Error(err, "failed to set controller reference on Job")
		return err
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Гонка: Job уже создан другим прогоном reconcile — не ошибка.
			return nil
		}
		logger.Error(err, "failed to create Job")
		return err
	}
	logger.Info("created training Job", "job", job.Name, "image", container.Image, "envCount", len(container.Env))
	return nil
}

// syncStatusFromJob отображает статусы Job/подов на Trial.Status.Phase.
func (r *TrialReconciler) syncStatusFromJob(ctx context.Context, logger logr.Logger, trial *mlv1.Trial, job *batchv1.Job) (ctrl.Result, error) {
	newPhase := phaseFromJob(job)

	// Phase=Pending до старта пода — не переписываем более ранние терминальные состояния.
	if newPhase == "" || (newPhase == mlv1.TrialPhasePending && trial.Status.Phase != "") {
		newPhase = trial.Status.Phase
	}

	changed := trial.Status.Phase != newPhase
	if changed {
		patched := trial.DeepCopy()
		patched.Status.Phase = newPhase
		if err := r.Status().Patch(ctx, patched, client.MergeFrom(trial)); err != nil {
			logger.Error(err, "failed to patch Trial status", "phase", newPhase)
			return ctrl.Result{}, err
		}
		logger.Info("updated Trial phase", "from", trial.Status.Phase, "to", newPhase)
		trial = patched
	}

	// Терминальная фаза: фиксируем время завершения и наблюдаем длительность.
	if newPhase == mlv1.TrialPhaseSucceeded || newPhase == mlv1.TrialPhaseFailed {
		if trial.Status.CompletionTime == nil {
			completion := metav1.Now()
			patched := trial.DeepCopy()
			patched.Status.CompletionTime = &completion
			if err := r.Status().Patch(ctx, patched, client.MergeFrom(trial)); err != nil {
				return ctrl.Result{}, err
			}
			trial = patched
		}
		if trial.Status.StartTime != nil {
			seconds := completionTimeOf(trial).Sub(trial.Status.StartTime.Time).Seconds()
			if seconds > 0 {
				trialDurationSeconds.Observe(seconds)
			}
		}
		r.Recorder.Eventf(trial, corev1.EventTypeNormal, string(newPhase),
			"Trial %s finished training Job with phase %s", trial.Name, newPhase)
		// Терминальная фаза — дальнейшие reconcile не нужны (Job TTL удалит объект).
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: requeueAfterTrialJob}, nil
}

// reconcileDelete удаляет дочерний Job и снимает finalizer.
func (r *TrialReconciler) reconcileDelete(ctx context.Context, logger logr.Logger, trial *mlv1.Trial) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(trial, trialFinalizer) {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: trial.Name, Namespace: trial.Namespace},
		}
		policy := metav1.DeletePropagationBackground
		err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy})
		if err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete owned Job")
			return ctrl.Result{}, err
		}
		logger.Info("deleted owned training Job (or already gone)", "job", job.Name)

		patched := trial.DeepCopy()
		controllerutil.RemoveFinalizer(patched, trialFinalizer)
		if err := r.Patch(ctx, patched, client.MergeFrom(trial)); err != nil {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager регистрирует контроллер и owner-relations (Trial -> Job).
func (r *TrialReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mlv1.Trial{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// ---------- вспомогательные функции ----------

// hyperparamEnv конвертирует каждую пару key=value гиперпараметров в ENV-переменную.
func hyperparamEnv(hps map[string]string) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(hps))
	for k, v := range hps {
		env = append(env, corev1.EnvVar{Name: EnvVarName(k), Value: v})
	}
	return env
}

// EnvVarName превращает имя гиперпараметра в валидное имя ENV-переменной:
// "learning_rate" -> "HP_LEARNING_RATE", "dropout-rate" -> "HP_DROPOUT_RATE".
func EnvVarName(hyperparam string) string {
	const prefix = "HP_"
	out := make([]byte, 0, len(hyperparam)+len(prefix))
	out = append(out, prefix...)
	for i := 0; i < len(hyperparam); i++ {
		c := hyperparam[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A' // приводим к верхнему регистру
			}
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// phaseFromJob маппит условия batchv1.Job на фазу Trial.
func phaseFromJob(job *batchv1.Job) mlv1.TrialPhase {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return mlv1.TrialPhaseSucceeded
		case batchv1.JobFailed:
			return mlv1.TrialPhaseFailed
		}
	}
	if job.Status.Active > 0 {
		return mlv1.TrialPhaseRunning
	}
	if job.Status.StartTime != nil {
		return mlv1.TrialPhaseRunning
	}
	return mlv1.TrialPhasePending
}

func completionTimeOf(trial *mlv1.Trial) time.Time {
	if trial.Status.CompletionTime != nil {
		return trial.Status.CompletionTime.Time
	}
	return time.Now()
}

func trialLabels(trial *mlv1.Trial) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "ml-experiment-operator",
		"ml.example.com/experiment":    trial.Spec.ExperimentRef,
		"ml.example.com/trial":         trial.Name,
	}
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func ptrInt32(v int32) *int32 { return &v }
