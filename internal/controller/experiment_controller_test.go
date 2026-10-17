// internal/controller/experiment_controller_test.go — unit-тесты ExperimentReconciler
// (Шаг 11) на fake-клиенте sigs.k8s.io/controller-runtime/pkg/client/fake с
// ассертами Gomega. Тест-кейсы:
//  1. Создание Experiment -> Phase становится Pending.
//  2. Reconcile в фазе Pending -> создаются Trial CR'ы (проверяется количество).
//  3. Все Trials Succeeded -> Phase становится Succeeded, BestTrial заполнен.
//  4. Удаление Experiment -> finalizer снимается, дочерние Trials удаляются.
package controller

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
	"github.com/myorg/ml-experiment-operator/internal/search"
)

const (
	testNamespace = "default"
	testExpName   = "exp1"
)

// newScheme собирает схему со стандартными k8s-типами и нашим CRD-пакетом api/v1.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	g := gomega.NewWithT(t)
	g.Expect(clientgoscheme.AddToScheme(s)).To(gomega.Succeed())
	g.Expect(mlv1.AddToScheme(s)).To(gomega.Succeed())
	return s
}

// newExperiment возвращает валидный базовый Experiment (grid 2x2, maxParallel=2).
func newExperiment() *mlv1.Experiment {
	return &mlv1.Experiment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testExpName,
			Namespace: testNamespace,
			// Финализатор заранее проставлен, чтобы первый же Reconcile
			// дошёл до установки Phase=Pending (его добавление — отдельный
			// кейс TestNewExperimentBecomesPending).
			Finalizers: []string{experimentFinalizer},
		},
		Spec: mlv1.ExperimentSpec{
			Image:   "trainer:v1",
			Dataset: mlv1.PVCSource{ClaimName: "dataset-pvc"},
			Search: mlv1.SearchConfig{
				Strategy: mlv1.SearchStrategyGrid,
				Parameters: map[string][]string{
					"lr":    {"0.01", "0.1"},
					"epoch": {"10", "20"},
				},
				MaxTrials:   4,
				MaxParallel: 2,
			},
			MaxRetries:    1,
			EarlyStopping: &mlv1.EarlyStoppingConfig{Metric: "val_loss"},
		},
	}
}

// baseExp — алиас для старых тестов (experiment_phase_test.go).
func baseExp() *mlv1.Experiment { return newExperiment() }

// req — алиас для старых тестов (experiment_phase_test.go).
func req() ctrl.Request { return expRequest() }

// newFakeReconciler строит reconciler поверх fake-клиента с переданными объектами.
func newFakeReconciler(t *testing.T, objs ...client.Object) *ExperimentReconciler {
	t.Helper()
	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()
	return &ExperimentReconciler{
		Client:         c,
		Scheme:         s,
		SearchRegistry: search.DefaultRegistry(),
	}
}

// expRequest — ctrl.Request для тестового эксперимента.
func expRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: testExpName, Namespace: testNamespace}}
}

// getExperiment — хелпер чтения Experiment из fake-клиента.
func getExperiment(g *gomega.WithT, c client.Client) *mlv1.Experiment {
	var got mlv1.Experiment
	g.Expect(c.Get(context.TODO(), types.NamespacedName{Name: testExpName, Namespace: testNamespace}, &got)).To(gomega.Succeed())
	return &got
}

// makeTrial — дочерний Trial данного эксперимента с указанным индексом/фазой/метриками.
func makeTrial(idx int, phase mlv1.TrialPhase, metrics map[string]float64, hyperparams map[string]string) *mlv1.Trial {
	return &mlv1.Trial{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testExpName + "-" + itoa(idx),
			Namespace: testNamespace,
			Labels: map[string]string{
				labelExperiment: testExpName,
				labelTrialIndex: itoa(idx),
			},
		},
		Spec: mlv1.TrialSpec{
			ExperimentRef:   testExpName,
			Image:           "trainer:v1",
			Hyperparameters: hyperparams,
		},
		Status: mlv1.TrialStatus{Phase: phase, Metrics: metrics},
	}
}

// ---------------------------------------------------------------------------
// Тест-кейс 1: создание Experiment -> Phase становится Pending.
// ---------------------------------------------------------------------------

// Новый эксперимент без статуса: первый проход добавляет finalizer,
// второй — переводит Phase из "" в Pending.
func TestNewExperimentBecomesPending(t *testing.T) {
	g := gomega.NewWithT(t)
	exp := newExperiment()
	exp.Finalizers = nil // стартуем совсем с чистого CR
	r := newFakeReconciler(t, exp)

	// Проход 1: add finalizer.
	res, err := r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res).To(gomega.Equal(ctrl.Result{}))

	got := getExperiment(g, r.Client)
	g.Expect(got.Finalizers).To(gomega.ContainElement(experimentFinalizer),
		"reconciler must add its cleanup finalizer on the first pass")
	g.Expect(got.Status.Phase).To(gomega.BeEmpty(),
		"phase is set only after the finalizer exists")

	// Проход 2: Phase "" -> Pending.
	res, err = r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res).To(gomega.Equal(ctrl.Result{}))

	got = getExperiment(g, r.Client)
	g.Expect(got.Status.Phase).To(gomega.Equal(mlv1.ExperimentPhasePending))
	g.Expect(got.Status.Conditions).NotTo(gomega.BeEmpty())
}

// NotFound — не ошибка: пустой результат, никакого паник/requeue.
func TestReconcileMissingExperimentIsNoop(t *testing.T) {
	g := gomega.NewWithT(t)
	r := newFakeReconciler(t) // пустой кластер

	res, err := r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res).To(gomega.Equal(ctrl.Result{}))
}

// ---------------------------------------------------------------------------
// Тест-кейс 2: Reconcile в фазе Pending -> создаются Trial CR'ы.
// ---------------------------------------------------------------------------

func TestPendingPhaseCreatesTrials(t *testing.T) {
	g := gomega.NewWithT(t)
	exp := newExperiment()
	exp.Status.Phase = mlv1.ExperimentPhasePending
	r := newFakeReconciler(t, exp)

	_, err := r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Статус: пространство поиска развёрнуто, фаза Running.
	got := getExperiment(g, r.Client)
	g.Expect(got.Status.Phase).To(gomega.Equal(mlv1.ExperimentPhaseRunning))
	g.Expect(got.Status.Trials.Total).To(gomega.Equal(int32(4)),
		"grid 2x2 must expand to 4 combinations")

	// Количество Trial CR'ов ограничено MaxParallel = 2.
	var trials mlv1.TrialList
	g.Expect(r.List(context.TODO(), &trials, client.InNamespace(testNamespace))).To(gomega.Succeed())
	g.Expect(trials.Items).To(gomega.HaveLen(2),
		"exactly maxParallel trials must be created from Pending")

	// Каждый trial: ownerReference на Experiment + корректные гиперпараметры.
	for i := range trials.Items {
		tr := &trials.Items[i]
		g.Expect(tr.OwnerReferences).To(gomega.HaveLen(1))
		g.Expect(tr.OwnerReferences[0].Kind).To(gomega.Equal("Experiment"))
		g.Expect(tr.OwnerReferences[0].Name).To(gomega.Equal(testExpName))
		g.Expect(tr.OwnerReferences[0].Controller).NotTo(gomega.BeNil())
		g.Expect(*tr.OwnerReferences[0].Controller).To(gomega.BeTrue(),
			"ownerReference must be a controller reference for garbage collection")
		g.Expect(tr.Labels).To(gomega.HaveKeyWithValue(labelExperiment, testExpName))
		g.Expect(tr.Spec.Hyperparameters).To(gomega.HaveLen(2),
			"each combination must pin all hyperparameters")
	}

	// Счётчик Pending отражает созданные trials.
	got = getExperiment(g, r.Client)
	g.Expect(got.Status.Trials.Pending).To(gomega.Equal(int32(2)))
}

// ---------------------------------------------------------------------------
// Тест-кейс 3: все Trials Succeeded -> Phase=Succeeded, BestTrial заполнен.
// ---------------------------------------------------------------------------

func TestAllTrialsSucceededSetsBestTrial(t *testing.T) {
	g := gomega.NewWithT(t)
	exp := newExperiment()
	exp.Status.Phase = mlv1.ExperimentPhaseRunning
	exp.Status.Trials.Total = 4

	hp := func(lr, epoch string) map[string]string {
		return map[string]string{"lr": lr, "epoch": epoch}
	}
	// 4 успешных trial'а; лучший по минимальному val_loss — exp1-1 (0.42).
	trials := []client.Object{
		makeTrial(0, mlv1.TrialPhaseSucceeded, map[string]float64{"val_loss": 0.50, "accuracy": 0.88}, hp("0.01", "10")),
		makeTrial(1, mlv1.TrialPhaseSucceeded, map[string]float64{"val_loss": 0.42, "accuracy": 0.95}, hp("0.1", "10")),
		makeTrial(2, mlv1.TrialPhaseSucceeded, map[string]float64{"val_loss": 0.61, "accuracy": 0.80}, hp("0.01", "20")),
		makeTrial(3, mlv1.TrialPhaseSucceeded, map[string]float64{"val_loss": 0.55, "accuracy": 0.90}, hp("0.1", "20")),
	}
	r := newFakeReconciler(t, append([]client.Object{exp}, trials...)...)

	res, err := r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res.Requeue).To(gomega.BeFalse())
	g.Expect(res.RequeueAfter).To(gomega.BeZero(),
		"terminal experiment must not be requeued")

	got := getExperiment(g, r.Client)
	g.Expect(got.Status.Phase).To(gomega.Equal(mlv1.ExperimentPhaseSucceeded))

	// BestTrial заполнен: имя, метрика и гиперпараметры лучшего trial'а.
	g.Expect(got.Status.BestTrial.Name).To(gomega.Equal("exp1-1"))
	g.Expect(got.Status.BestTrial.Metrics).To(gomega.HaveKeyWithValue("val_loss", 0.42))
	g.Expect(got.Status.BestTrial.Hyperparameters).To(gomega.Equal(map[string]string{"lr": "0.1", "epoch": "10"}))

	// Агрегированные счётчики.
	g.Expect(got.Status.Trials.Succeeded).To(gomega.Equal(int32(4)))
	g.Expect(got.Status.Trials.Failed).To(gomega.Equal(int32(0)))
	g.Expect(got.Status.Trials.Running).To(gomega.Equal(int32(0)))
}

// ---------------------------------------------------------------------------
// Тест-кейс 4: удаление Experiment -> finalizer снят, дочерние Trials удалены.
// ---------------------------------------------------------------------------

func TestDeletionCleansUpTrialsAndRemovesFinalizer(t *testing.T) {
	g := gomega.NewWithT(t)
	exp := newExperiment()
	now := metav1.Now()
	exp.DeletionTimestamp = &now // имитация удаления, блокируемого нашим finalizer'ом

	trial := makeTrial(0, mlv1.TrialPhaseRunning, nil, map[string]string{"lr": "0.01"})
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: trial.Name, Namespace: testNamespace},
	}
	r := newFakeReconciler(t, exp, trial, pvc)

	res, err := r.Reconcile(context.TODO(), expRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(res).To(gomega.Equal(ctrl.Result{}))

	// Дочерние Trial CR'ы удалены.
	var remaining mlv1.TrialList
	g.Expect(r.List(context.TODO(), &remaining, client.InNamespace(testNamespace))).To(gomega.Succeed())
	g.Expect(remaining.Items).To(gomega.BeEmpty(),
		"cleanup must delete all child trials")

	// PVC trial'а удалён.
	var pvcs corev1.PersistentVolumeClaimList
	g.Expect(r.List(context.TODO(), &pvcs, client.InNamespace(testNamespace))).To(gomega.Succeed())
	g.Expect(pvcs.Items).To(gomega.BeEmpty(),
		"cleanup must delete trial PVCs")

	// Finalizer снят: либо объект ещё жив и не содержит нашего finalizer'а,
	// либо fake-клиент уже удалил его целиком (finalizer список пуст).
	var gone mlv1.Experiment
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: testExpName, Namespace: testNamespace}, &gone)
	if err == nil {
		g.Expect(gone.Finalizers).NotTo(gomega.ContainElement(experimentFinalizer),
			"finalizer must be removed after cleanup")
	} else {
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
	}
}
