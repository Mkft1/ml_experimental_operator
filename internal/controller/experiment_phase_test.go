// internal/controller/experiment_phase_test.go — тесты логики фаз Pending/Running
// (Шаг 6): разворачивание пространства поиска, лимит MaxParallel, BestTrial,
// переход в Failed по бюджету ретраев.
package controller

import (
	"context"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
	"github.com/myorg/ml-experiment-operator/internal/search"
)

func gridExp() *mlv1.Experiment {
	e := baseExp() // strategy=grid, maxTrials=4, maxParallel=2
	e.Spec.Search.Parameters = map[string][]string{
		"lr":    {"0.01", "0.1"},
		"epoch": {"10", "20"},
	}
	e.Spec.EarlyStopping = &mlv1.EarlyStoppingConfig{Metric: "val_loss"}
	e.Finalizers = []string{experimentFinalizer}
	return e
}

func withPendingPhase(e *mlv1.Experiment) *mlv1.Experiment {
	e.Status.Phase = mlv1.ExperimentPhasePending
	return e
}

func newReconciler(t *testing.T, objs ...client.Object) (*ExperimentReconciler, *fake.ClientBuilder) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	r := &ExperimentReconciler{Client: c, Scheme: s, SearchRegistry: search.DefaultRegistry()}
	return r, nil
}

// Pending -> Running: Total = len(combinations), первые MaxParallel trials созданы.
func TestPendingExpandsSearchSpaceAndStartsTrials(t *testing.T) {
	exp := withPendingPhase(gridExp())
	r, _ := newReconciler(t, exp)

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}

	var got mlv1.Experiment
	if err := r.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != mlv1.ExperimentPhaseRunning {
		t.Fatalf("phase=%q want Running", got.Status.Phase)
	}
	if got.Status.Trials.Total != 4 {
		t.Fatalf("total=%d want 4 (2x2 grid)", got.Status.Trials.Total)
	}

	var tl mlv1.TrialList
	if err := r.List(context.TODO(), &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Items) != 2 {
		t.Fatalf("created trials=%d want 2 (maxParallel)", len(tl.Items))
	}
	for i := range tl.Items {
		tr := &tl.Items[i]
		if tr.OwnerReferences == nil || tr.OwnerReferences[0].Kind != "Experiment" ||
			tr.OwnerReferences[0].Name != "exp1" || !*tr.OwnerReferences[0].Controller {
			t.Fatalf("trial %s missing controller ownerReference on Experiment", tr.Name)
		}
		if tr.Labels[labelExperiment] != "exp1" {
			t.Fatalf("trial %s missing experiment label", tr.Name)
		}
		if len(tr.Spec.Hyperparameters) != 2 {
			t.Fatalf("trial %s hyperparameters=%v", tr.Name, tr.Spec.Hyperparameters)
		}
	}
	if got.Status.Trials.Pending != 2 {
		t.Fatalf("pending=%d want 2", got.Status.Trials.Pending)
	}
}

// Все trials завершены -> Succeeded + BestTrial по минимальному val_loss.
func TestRunningCompletesWithBestTrial(t *testing.T) {
	exp := gridExp()
	exp.Spec.MaxRetries = 1 // бюджет отказов: 1 * 4 = 4, один failed допустим
	exp.Status.Phase = mlv1.ExperimentPhaseRunning
	exp.Status.Trials.Total = 4

	mk := func(idx int32, phase mlv1.TrialPhase, loss float64) *mlv1.Trial {
		tr := &mlv1.Trial{
			ObjectMeta: metav1.ObjectMeta{
				Name: "exp1-" + string(rune('0'+idx)), Namespace: "default",
				Labels: map[string]string{labelExperiment: "exp1", labelTrialIndex: itoa(int(idx))},
			},
			Spec:   mlv1.TrialSpec{ExperimentRef: "exp1", Image: "trainer:v1"},
			Status: mlv1.TrialStatus{Phase: phase},
		}
		if phase == mlv1.TrialPhaseSucceeded {
			tr.Status.Metrics = map[string]float64{"val_loss": loss, "accuracy": 0.9}
		}
		return tr
	}
	objs := []client.Object{
		exp,
		mk(0, mlv1.TrialPhaseSucceeded, 0.50),
		mk(1, mlv1.TrialPhaseSucceeded, 0.42), // лучший
		mk(2, mlv1.TrialPhaseSucceeded, 0.61),
		mk(3, mlv1.TrialPhaseFailed, 0),
	}
	r, _ := newReconciler(t, objs...)
	// У trial-1 гиперпараметры должны быть заданы для проверки BestTrial.Hyperparameters.
	var trs mlv1.TrialList
	_ = r.List(context.TODO(), &trs)
	for i := range trs.Items {
		trs.Items[i].Spec.Hyperparameters = map[string]string{"lr": "0.1", "epoch": "10"}
		_ = r.Update(context.TODO(), &trs.Items[i])
	}

	res, err := r.Reconcile(context.TODO(), req())
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter > 0 || res.Requeue {
		t.Fatalf("terminal reconcile should not requeue: %+v", res)
	}

	var got mlv1.Experiment
	if err := r.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != mlv1.ExperimentPhaseSucceeded {
		t.Fatalf("phase=%q want Succeeded", got.Status.Phase)
	}
	if got.Status.BestTrial.Name != "exp1-1" {
		t.Fatalf("best=%q want exp1-1 (min val_loss)", got.Status.BestTrial.Name)
	}
	if got.Status.BestTrial.Metrics["val_loss"] != 0.42 {
		t.Fatalf("best metrics=%v", got.Status.BestTrial.Metrics)
	}
	if got.Status.BestTrial.Hyperparameters["lr"] != "0.1" {
		t.Fatalf("best hyperparams=%v", got.Status.BestTrial.Hyperparameters)
	}
	if got.Status.Trials.Succeeded != 3 || got.Status.Trials.Failed != 1 {
		t.Fatalf("counters=%+v", got.Status.Trials)
	}
}

// Failed > MaxRetries*Total -> Phase=Failed, новые trials не создаются.
func TestRunningFailsOnRetryBudgetExceeded(t *testing.T) {
	exp := gridExp()
	exp.Spec.MaxRetries = 0 // бюджет = 0
	exp.Status.Phase = mlv1.ExperimentPhaseRunning
	exp.Status.Trials.Total = 4

	failing := &mlv1.Trial{
		ObjectMeta: metav1.ObjectMeta{
			Name: "exp1-0", Namespace: "default",
			Labels: map[string]string{labelExperiment: "exp1", labelTrialIndex: "0"},
		},
		Spec:   mlv1.TrialSpec{ExperimentRef: "exp1", Image: "trainer:v1"},
		Status: mlv1.TrialStatus{Phase: mlv1.TrialPhaseFailed},
	}
	r, _ := newReconciler(t, exp, failing)

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}
	var got mlv1.Experiment
	if err := r.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != mlv1.ExperimentPhaseFailed {
		t.Fatalf("phase=%q want Failed", got.Status.Phase)
	}
	var tl mlv1.TrialList
	_ = r.List(context.TODO(), &tl)
	if len(tl.Items) != 1 {
		t.Fatalf("no new trials expected after failure, got %d", len(tl.Items))
	}
}

// Running не должен превышать MaxParallel одновременно запущенных trials.
func TestRunningRespectsMaxParallel(t *testing.T) {
	exp := gridExp()
	exp.Status.Phase = mlv1.ExperimentPhaseRunning
	exp.Status.Trials.Total = 4

	running := func(idx int) *mlv1.Trial {
		return &mlv1.Trial{
			ObjectMeta: metav1.ObjectMeta{
				Name: "exp1-" + itoa(idx), Namespace: "default",
				Labels: map[string]string{labelExperiment: "exp1", labelTrialIndex: itoa(idx)},
			},
			Spec:   mlv1.TrialSpec{ExperimentRef: "exp1", Image: "trainer:v1"},
			Status: mlv1.TrialStatus{Phase: mlv1.TrialPhaseRunning},
		}
	}
	r, _ := newReconciler(t, exp, running(0), running(1))

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}
	var tl mlv1.TrialList
	_ = r.List(context.TODO(), &tl)
	if len(tl.Items) != 2 {
		t.Fatalf("running==maxParallel -> no new trials, got %d", len(tl.Items))
	}
	var got mlv1.Experiment
	_ = r.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got)
	if got.Status.Trials.Running != 2 {
		t.Fatalf("running=%d want 2", got.Status.Trials.Running)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
