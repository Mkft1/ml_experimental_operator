// internal/controller/experiment_controller.go — контроллер Experiment:
// жизненный цикл CR (finalizer, cleanup) и оркестрация поиска гиперпараметров
// (фаза Pending — разворачивание пространства поиска; фаза Running — создание
// Trial CR'ов в пределах MaxParallel, агрегация статусов, выбор BestTrial,
// переходы в Succeeded/Failed).
package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
	"github.com/myorg/ml-experiment-operator/internal/search"
)

const (
	// experimentFinalizer гарантирует, что мы успеем очистить дочерние
	// ресурсы (Trial CR'ы и их PVC) до фактического удаления Experiment.
	experimentFinalizer = "experiment.ml.example.com/cleanup"

	// labelExperiment — метка, по которой находим все дочерние Trial данного эксперимента.
	labelExperiment = "ml.example.com/experiment"

	// annotationOutputPVC — имя output-PVC, созданного оператором для эксперимента
	// (кластерный create пока не выполняется — аннотация резервируется под cleanup).
	annotationOutputPVC = "ml.example.com/output-pvc"

	// labelTrialIndex — индекс комбинации гиперпараметров в пространстве поиска.
	labelTrialIndex = "ml.example.com/trial-index"

	// defaultRequeueAfter — периодический пересчёт статуса, если watcher по Trial
	// почему-то не сработал (страховка от потерянных событий).
	defaultRequeueAfter = 15 * time.Second
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

// +kubebuilder:rbac:groups=ml.example.com,resources=experiments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ml.example.com,resources=experiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ml.example.com,resources=experiments/finalizers,verbs=update
// +kubebuilder:rbac:groups=ml.example.com,resources=trials,verbs=get;list;watch;create;update;patch;delete
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
		experimentPhaseTransitionsTotal.WithLabelValues(string(mlv1.ExperimentPhasePending)).Inc()
		logger.Info("Experiment initialized in phase Pending")
		return ctrl.Result{}, nil
	}

	// 5. Фаза Pending: развернуть пространство поиска и перейти в Running.
	if exp.Status.Phase == mlv1.ExperimentPhasePending {
		return r.reconcilePending(ctx, &exp, logger)
	}

	// 6. Фаза Running: создать недостающие Trials в пределах MaxParallel,
	// агрегировать статусы, выбрать BestTrial, завершить эксперимент.
	if exp.Status.Phase == mlv1.ExperimentPhaseRunning {
		return r.reconcileRunning(ctx, &exp, logger)
	}

	// 7. Терминальные фазы (Succeeded/Failed) — ничего не делаем.
	logger.V(1).Info("experiment in terminal phase, nothing to do", "phase", exp.Status.Phase)
	return ctrl.Result{}, nil
}

// reconcilePending — логика фазы Pending (Шаг 6, п.1):
//   - строит стратегию поиска через SearchRegistry (grid или random);
//   - разворачивает пространство гиперпараметров в список комбинаций;
//   - ставит Status.Trials.Total = len(combinations);
//   - переводит эксперимент в Phase = Running.
func (r *ExperimentReconciler) reconcilePending(ctx context.Context, exp *mlv1.Experiment, logger logr.Logger) (ctrl.Result, error) {
	strategy, err := r.SearchRegistry.Build(&exp.Spec)
	if err != nil {
		logger.Error(err, "failed to build search strategy")
		r.recordWarning(exp, "SearchStrategyFailed", "failed to build search strategy %q: %v", exp.Spec.Search.Strategy, err)
		setExperimentCondition(exp, "Ready", metav1.ConditionFalse, "InvalidSearchSpace", err.Error())
		if serr := r.Status().Update(ctx, exp); serr != nil {
			return ctrl.Result{}, serr
		}
		// Пространство поиска невалидно — это не транзиентная ошибка кластера,
		// requeue без backoff-ошибки не нужен; ждём изменения spec.
		return ctrl.Result{}, nil
	}

	combinations := make([]map[string]string, 0, strategy.Total())
	for i := 0; ; i++ {
		c, ok := strategy.Peek(i)
		if !ok {
			break
		}
		combinations = append(combinations, c)
	}

	total := int32(len(combinations))
	exp.Status.Trials.Total = total
	exp.Status.Phase = mlv1.ExperimentPhaseRunning
	setExperimentCondition(exp, "Ready", metav1.ConditionTrue, "Running",
		fmt.Sprintf("search space expanded: %d combinations, running with maxParallel=%d",
			total, exp.Spec.Search.MaxParallel))
	if err := r.Status().Update(ctx, exp); err != nil {
		logger.Error(err, "failed to transition Pending -> Running")
		return ctrl.Result{}, err
	}
	experimentPhaseTransitionsTotal.WithLabelValues(string(mlv1.ExperimentPhaseRunning)).Inc()
	logger.Info("transitioned to Running", "strategy", strategy.Name(), "totalCombinations", total)

	// Сразу идём в Running-проход: создадим первые trials, не дожидаясь события.
	return r.reconcileRunning(ctx, exp, logger)
}

// reconcileRunning — логика фазы Running (Шаг 6, п.2-4):
//   - считает счётчики Trial по фазах и пишет их в Status.Trials;
//   - если Running < MaxParallel и есть несозданные комбинации — создаёт
//     новые Trial CR'ы (до лимита) с OwnerReference на Experiment;
//   - если все комбинации созданы и все trials завершены — определяет исход:
//     слишком много Failed (> MaxRetries*Total) -> Phase=Failed;
//     иначе — лучший trial по EarlyStopping.Metric -> Status.BestTrial, Phase=Succeeded.
func (r *ExperimentReconciler) reconcileRunning(ctx context.Context, exp *mlv1.Experiment, logger logr.Logger) (ctrl.Result, error) {
	// Пространство поиска детерминированно пересчитываем из spec (см. search.Combinations):
	// i-я комбинация соответствует trial "<exp.name>-<i>".
	strategy, err := r.SearchRegistry.Build(&exp.Spec)
	if err != nil {
		logger.Error(err, "failed to rebuild search strategy in Running phase")
		return ctrl.Result{}, err
	}
	total := strategy.Total()
	if total == 0 {
		// Практически недостижимо (Pending бы не прошёл), но защищаемся.
		exp.Status.Phase = mlv1.ExperimentPhaseFailed
		setExperimentCondition(exp, "Ready", metav1.ConditionFalse, "EmptySearchSpace",
			"search space produced zero combinations")
		if err := r.Status().Update(ctx, exp); err != nil {
			return ctrl.Result{}, err
		}
		experimentPhaseTransitionsTotal.WithLabelValues(string(mlv1.ExperimentPhaseFailed)).Inc()
		return ctrl.Result{}, nil
	}

	// 1. Посчитать текущие Trial'ы по фазам.
	var trials mlv1.TrialList
	if err := r.List(ctx, &trials,
		client.InNamespace(exp.Namespace),
		client.MatchingLabels{labelExperiment: exp.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list child trials: %w", err)
	}

	var pending, running, succeeded, failed int32
	createdByIndex := make(map[int32]struct{}, len(trials.Items))
	completed := make([]*mlv1.Trial, 0, len(trials.Items))
	for i := range trials.Items {
		t := &trials.Items[i]
		if idx, perr := parseTrialIndex(t.Labels[labelTrialIndex]); perr == nil {
			createdByIndex[idx] = struct{}{}
		}
		switch t.Status.Phase {
		case mlv1.TrialPhasePending:
			pending++
		case mlv1.TrialPhaseRunning:
			running++
		case mlv1.TrialPhaseSucceeded:
			succeeded++
			completed = append(completed, t)
		case mlv1.TrialPhaseFailed:
			failed++
			completed = append(completed, t)
		default: // "" — ещё не обработан контроллером Trial
			pending++
		}
	}

	countersChanged := exp.Status.Trials.Pending != pending ||
		exp.Status.Trials.Running != running ||
		exp.Status.Trials.Succeeded != succeeded ||
		exp.Status.Trials.Failed != failed ||
		exp.Status.Trials.Total != int32(total)
	exp.Status.Trials.Pending = pending
	exp.Status.Trials.Running = running
	exp.Status.Trials.Succeeded = succeeded
	exp.Status.Trials.Failed = failed
	exp.Status.Trials.Total = int32(total)

	// 2. Порог отказов: Failed > MaxRetries * Total -> эксперимент считается Failed.
	// Проверяем до создания новых trials, чтобы не запускать лишние обучения.
	failureBudget := exp.Spec.MaxRetries * int32(total)
	if failed > failureBudget {
		exp.Status.Phase = mlv1.ExperimentPhaseFailed
		setExperimentCondition(exp, "Ready", metav1.ConditionFalse, "TooManyFailures",
			fmt.Sprintf("%d failed trials exceed retry budget maxRetries(%d) * total(%d) = %d",
				failed, exp.Spec.MaxRetries, total, failureBudget))
		if err := r.Status().Update(ctx, exp); err != nil {
			return ctrl.Result{}, err
		}
		experimentPhaseTransitionsTotal.WithLabelValues(string(mlv1.ExperimentPhaseFailed)).Inc()
		r.recordWarning(exp, "TooManyFailures",
			"%d failed trials exceed retry budget (%d), marking experiment Failed", failed, failureBudget)
		logger.Info("experiment failed: retry budget exhausted", "failed", failed, "budget", failureBudget)
		return ctrl.Result{}, nil
	}

	// 3. Все комбинации созданы и все trials завершены -> определить итог.
	allCreated := len(trials.Items) >= total
	allCompleted := allCreated && (succeeded+failed) >= int32(total) && running == 0 && pending == 0
	if allCompleted {
		best := pickBestTrial(exp, completed)
		exp.Status.BestTrial = best
		exp.Status.Phase = mlv1.ExperimentPhaseSucceeded
		setExperimentCondition(exp, "Ready", metav1.ConditionTrue, "Succeeded",
			fmt.Sprintf("all %d trials completed; best trial %q", total, best.Name))
		if err := r.Status().Update(ctx, exp); err != nil {
			return ctrl.Result{}, err
		}
		experimentPhaseTransitionsTotal.WithLabelValues(string(mlv1.ExperimentPhaseSucceeded)).Inc()
		r.recordNormal(exp, "Succeeded", "experiment finished, best trial: %s", best.Name)
		logger.Info("experiment succeeded", "bestTrial", best.Name)
		return ctrl.Result{}, nil
	}

	// 4. Создать новые Trial CR'ы, если есть свободные слоты параллельности.
	created := 0
	maxParallel := exp.Spec.Search.MaxParallel
	if maxParallel < 1 {
		maxParallel = 1
	}
	for idx := 0; idx < total && int32(created)+running < maxParallel; idx++ {
		i := int32(idx)
		if _, exists := createdByIndex[i]; exists {
			continue // этот слот уже занят trial'ом
		}
		hyperparams, ok := strategy.Peek(idx)
		if !ok {
			break
		}
		trialName := fmt.Sprintf("%s-%d", exp.Name, idx)
		trial := &mlv1.Trial{
			ObjectMeta: metav1.ObjectMeta{
				Name:      trialName,
				Namespace: exp.Namespace,
				Labels: map[string]string{
					labelExperiment: exp.Name,
					labelTrialIndex: strconv.Itoa(idx),
				},
			},
			Spec: mlv1.TrialSpec{
				ExperimentRef:   exp.Name,
				Hyperparameters: hyperparams,
				Command:         exp.Spec.Command,
				Image:           exp.Spec.Image,
				Resources:       exp.Spec.Resources.DeepCopy(),
				Attempt:         1,
			},
		}
		// OwnerReference на Experiment: GC кластера подчистит trials при удалении,
		// а watcher Owns(&Trial{}) триггерит reconcile эксперимента на их события.
		if err := ctrl.SetControllerReference(exp, trial, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set owner reference on trial %s: %w", trialName, err)
		}
		if err := r.Create(ctx, trial); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue // гонка/ретрай — слот уже занят, идём дальше
			}
			return ctrl.Result{}, fmt.Errorf("create trial %s: %w", trialName, err)
		}
		exp.Status.Trials.Pending++ // новый trial стартует в Pending
		created++
		createdByIndex[i] = struct{}{}
		experimentTrialCreatedTotal.WithLabelValues(string(exp.Spec.Search.Strategy)).Inc()
		logger.V(1).Info("created trial", "name", trialName, "index", idx)
	}

	// 5. Сохранить обновлённый статус (счётчики + возможно созданные trials).
	if created > 0 || countersChanged {
		if err := r.Status().Update(ctx, exp); err != nil {
			logger.Error(err, "failed to update experiment status")
			return ctrl.Result{}, err
		}
	}

	// Страховочный requeue:обычно нас триггерят события Trial (Owns), но на
	// случай потерянных событий пересчитаемся через defaultRequeueAfter.
	return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
}

// pickBestTrial выбирает лучший завершённый trial по метрике
// spec.earlyStopping.metric. Правила:
//   - учитываются только Succeeded trials;
//   - имя метрики приводится к нижнему регистру (val_loss / Val_Loss — одно и то же);
//   - метрика вида *_loss/*_error/*err* минимизируется, остальные максимизируются;
//   - если earlyStopping не задан или ни один trial не содержит метрику —
//     возвращается первый Succeeded trial (или пустой BestTrialInfo).
func pickBestTrial(exp *mlv1.Experiment, trials []*mlv1.Trial) mlv1.BestTrialInfo {
	metric := earlyStoppingMetric(exp.Spec.EarlyStopping)
	minimize := strings.Contains(metric, "loss") ||
		strings.Contains(metric, "error") ||
		strings.Contains(metric, "err")

	var best *mlv1.Trial
	var bestVal float64
	for _, t := range trials {
		if t.Status.Phase != mlv1.TrialPhaseSucceeded {
			continue
		}
		if best == nil {
			best, bestVal = t, metricValue(t, metric, minimize)
			continue
		}
		v := metricValue(t, metric, minimize)
		if metric != "" {
			if (minimize && v < bestVal) || (!minimize && v > bestVal) {
				best, bestVal = t, v
			}
		} else if best.Status.CompletionTime == nil ||
			(t.Status.CompletionTime != nil && t.Status.CompletionTime.Before(best.Status.CompletionTime)) {
			// Без метрики берём самый быстрый успешный trial.
			best = t
		}
	}
	if best == nil {
		return mlv1.BestTrialInfo{}
	}
	return mlv1.BestTrialInfo{
		Name:            best.Name,
		Metrics:         best.Status.Metrics,
		Hyperparameters: best.Spec.Hyperparameters,
	}
}

// metricValue достаёт значение целевой метрики из trial (case-insensitive).
// Если метрика отсутствует, возвращает худшее возможное значение для данного
// направления оптимизации (minimize -> +макс, иначе -> -макс), чтобы такой
// trial гарантированно проиграл сравнение.
func metricValue(t *mlv1.Trial, metric string, minimize bool) float64 {
	if metric == "" {
		return 0
	}
	for k, v := range t.Status.Metrics {
		if strings.EqualFold(k, metric) {
			return v
		}
	}
	const sentinel = float64(1 << 30)
	if minimize {
		return sentinel
	}
	return -sentinel
}

// earlyStoppingMetric безопасно читает имя целевой метрики из опционального
// earlyStopping (nil-приёмлемый метод на *mlv1.EarlyStoppingConfig).
func earlyStoppingMetric(es *mlv1.EarlyStoppingConfig) string {
	if es == nil {
		return ""
	}
	return strings.ToLower(es.Metric)
}

// parseTrialIndex разбирает значение метки ml.example.com/trial-index.
func parseTrialIndex(s string) (int32, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// recordNormal шлёт Normal-событие (безопасно к nil Recorder).
func (r *ExperimentReconciler) recordNormal(exp *mlv1.Experiment, reason, messageFmt string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(exp, corev1.EventTypeNormal, reason, messageFmt, args...)
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
// Owns(&mlv1.Trial{}) — триггерит reconcile эксперимента на каждое изменение
// дочерних trials (завершение training job -> смена фазы -> освобождение слота
// параллельности / итоговый переход в Succeeded/Failed).
func (r *ExperimentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mlv1.Experiment{}).
		Owns(&mlv1.Trial{}).
		Watches(
			&source.Kind{Type: &mlv1.Trial{}},
			handler.EnqueueRequestsFromMapFunc(r.mapTrialToExperiment),
		).
		Complete(r)
}

// mapTrialToExperiment — для Trial без ownerReference (например созданных
// вручную) находим родительский Experiment по метке ml.example.com/experiment.
func (r *ExperimentReconciler) mapTrialToExperiment(o client.Object) []reconcile.Request {
	labels := o.GetLabels()
	name, ok := labels[labelExperiment]
	if !ok || name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{Namespace: o.GetNamespace(), Name: name},
	}}
}
