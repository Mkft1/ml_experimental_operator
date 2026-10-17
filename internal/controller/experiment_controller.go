// internal/controller/experiment_controller.go — контроллер Experiment (часть 1):
// жизненный цикл CR (finalizer, cleanup, начальная фаза). Сложная логика
// поиска/создания Trials будет добавлена на следующих шагах.
package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
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
	"github.com/myorg/ml-experiment-operator/internal/search"
)

const (
	// experimentFinalizer гарантирует, что мы успеем очистить дочерние
	// ресурсы (Trial CR'ы и их PVC) до фактического удаления Experiment.
	experimentFinalizer = "experiment.ml.myorg.dev/cleanup"

	// labelExperiment — метка, по которой находим все дочерние Trial данного эксперимента.
	labelExperiment = "ml.myorg.dev/experiment"

	// annotationOutputPVC — имя output-PVC, созданного оператором для эксперимента
	// (кластерный create пока не выполняется — аннотация резервируется под cleanup).
	annotationOutputPVC = "ml.myorg.dev/output-pvc"
)

// ExperimentReconciler reconciles an Experiment object.
type ExperimentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder шлёт Events (kubectl describe experiment) об ошибках/переходах фаз.
	Recorder record.EventRecorder
	// SearchRegistry — реестр стратегий поиска (grid/random).
	// Используется в части 2 при создании Trials; здесь только внедряется.
	SearchRegistry *search.Registry
}

// +kubebuilder:rbac:groups=ml.myorg.dev,resources=experiments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ml.myorg.dev,resources=experiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ml.myorg.dev,resources=experiments/finalizers,verbs=update
// +kubebuilder:rbac:groups=ml.myorg.dev,resources=trials,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile — цикл согласования Experiment.
//
// Пока реализована только каркасная часть жизненного цикла (Шаг 5):
// NotFound -> nil; удаление -> cleanup + снятие finalizer; новый -> Phase=Pending.
// Разворачивание пространства поиска и создание Trials — следующие шаги.
func (r *ExperimentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("experiment", req.NamespacedName)

	// 1. Получить Experiment CR.
	var exp mlv1.Experiment
	if err := r.Get(ctx, req.NamespacedName, &exp); err != nil {
		if apierrors.IsNotFound(err) {
			// Объект удалён из кластера — ничего делать не нужно, это не ошибка.
			logger.V(1).Info("Experiment not found, likely deleted; nothing to do")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Experiment")
		return ctrl.Result{}, err
	}

	// 2. Эксперимент помечен на удаление -> cleanup дочерних ресурсов, затем снимаем finalizer.
	if !exp.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&exp, experimentFinalizer) {
			if err := r.cleanup(ctx, &exp, logger); err != nil {
				logger.Error(err, "cleanup of child resources failed")
				r.recordWarning(&exp, "CleanupFailed",
					"failed to clean up child resources: %v", err)
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&exp, experimentFinalizer)
			if err := r.Update(ctx, &exp); err != nil {
				logger.Error(err, "failed to remove finalizer")
				return ctrl.Result{}, err
			}
			logger.Info("finalizer removed, Experiment deletion may proceed")
		}
		return ctrl.Result{}, nil
	}

	// 3. Если finalizer ещё нет — добавить (следующий проход увидит уже обновлённый объект).
	if !controllerutil.ContainsFinalizer(&exp, experimentFinalizer) {
		controllerutil.AddFinalizer(&exp, experimentFinalizer)
		if err := r.Update(ctx, &exp); err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		logger.V(1).Info("finalizer added")
		return ctrl.Result{}, nil
	}

	// 4. Новый эксперимент (Phase == "") -> перевести в Pending.
	if exp.Status.Phase == "" {
		exp.Status.Phase = mlv1.ExperimentPhasePending
		setExperimentCondition(&exp, "Ready", metav1.ConditionFalse, "Pending",
			"Experiment accepted by operator, waiting to schedule trials")
		if err := r.Status().Update(ctx, &exp); err != nil {
			logger.Error(err, "failed to set initial phase Pending")
			return ctrl.Result{}, err
		}
		logger.Info("Experiment initialized in phase Pending")
		return ctrl.Result{}, nil
	}

	//  5. TODO (часть 6): сложная логика — построение стратегии поиска через
	//     r.SearchRegistry, лимит MaxParallel, создание Trial CR'ов, агрегация статусов.
	//     Пока просто завершаем проход без requeue.
	logger.V(1).Info("reconcile finished (search/trial creation not implemented yet)",
		"phase", exp.Status.Phase)
	return ctrl.Result{}, nil
}

// cleanup удаляет все дочерние Trial CR'ы эксперимента и связанные с ними PVC.
// Идемпотентен: уже отсутствующие ресурсы молча пропускаются.
func (r *ExperimentReconciler) cleanup(ctx context.Context, exp *mlv1.Experiment, logger logr.Logger) error {
	// 1. Найти все Trial, принадлежащие этому Experiment (по метке).
	var trials mlv1.TrialList
	if err := r.List(ctx, &trials,
		client.InNamespace(exp.Namespace),
		client.MatchingLabels{labelExperiment: exp.Name},
	); err != nil {
		return fmt.Errorf("list child trials: %w", err)
	}

	for i := range trials.Items {
		trial := &trials.Items[i]

		// 1.1. Удалить PVC этого trial (имя совпадает с именем Trial CR).
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: trial.Name, Namespace: trial.Namespace},
		}
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete trial pvc %s/%s: %w", pvc.Namespace, pvc.Name, err)
		}

		// 1.2. Удалить сам Trial CR. Job'ы будут собраны GC через ownerReference
		// (контроллер Trial также корректно снимает свой finalizer при удалении).
		if err := r.Delete(ctx, trial); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete trial %s/%s: %w", trial.Namespace, trial.Name, err)
		}
	}

	// 2. Output-PVC эксперимента: он создаётся оператором только когда в spec.output
	// не указан существующий claim; тогда его имя пишется в аннотацию. Пользовательские
	// PVC (spec.output.pvc.claimName) мы НЕ удаляем — они нам не принадлежат.
	if pvcName, ok := exp.Annotations[annotationOutputPVC]; ok && pvcName != "" {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: exp.Namespace},
		}
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete output pvc %s/%s: %w", pvc.Namespace, pvc.Name, err)
		}
	}

	logger.Info("cleanup completed", "trialsDeleted", len(trials.Items))
	return nil
}

// setExperimentCondition — convenience-обёртка установки metav1.Condition в status
// (со стабильными Type/Reason/Message и ObservedGeneration).
func setExperimentCondition(exp *mlv1.Experiment, condType string, status metav1.ConditionStatus, reason, message string) {
	cond := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: exp.Generation,
	}
	for i, c := range exp.Status.Conditions {
		if c.Type == condType {
			exp.Status.Conditions[i] = cond
			return
		}
	}
	exp.Status.Conditions = append(exp.Status.Conditions, cond)
}

// recordWarning шлёт Warning-событие об ошибке (безопасно к nil Recorder).
func (r *ExperimentReconciler) recordWarning(exp *mlv1.Experiment, reason, messageFmt string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(exp, corev1.EventTypeWarning, reason, messageFmt, args...)
}

// SetupWithManager регистрирует контроллер Experiment у менеджера.
// На следующих шагах сюда добавится Owns(&mlv1.Trial{}) для триггеров от статуса trials.
func (r *ExperimentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mlv1.Experiment{}).
		Complete(r)
}
