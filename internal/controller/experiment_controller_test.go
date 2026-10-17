package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
	"github.com/myorg/ml-experiment-operator/internal/search"
)

func newScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := mlv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func baseExp() *mlv1.Experiment {
	return &mlv1.Experiment{
		ObjectMeta: metav1.ObjectMeta{Name: "exp1", Namespace: "default"},
		Spec: mlv1.ExperimentSpec{
			Image:   "trainer:v1",
			Dataset: mlv1.PVCSource{ClaimName: "ds"},
			Search:  mlv1.SearchConfig{Strategy: "grid", MaxTrials: 4, MaxParallel: 2},
		},
	}
}

func req() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: "exp1", Namespace: "default"}}
}

// Новый эксперимент: первый проход добавляет finalizer, второй ставит Phase=Pending.
func TestReconcileNewExperiment(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(baseExp()).Build()
	r := &ExperimentReconciler{Client: c, Scheme: s, SearchRegistry: search.NewRegistry()}

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}
	var got mlv1.Experiment
	_ = c.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got)
	if len(got.Finalizers) == 0 {
		t.Fatalf("finalizer not added")
	}

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got)
	if got.Status.Phase != mlv1.ExperimentPhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
	// Третий проход (фаза уже Pending) — просто ctrl.Result{}, без ошибки.
	res, err := r.Reconcile(context.TODO(), req())
	if err != nil || res.Requeue || res.RequeueAfter > 0 {
		t.Fatalf("unexpected result %+v err %v", res, err)
	}
}

// NotFound -> nil, без ошибки.
func TestReconcileNotFound(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &ExperimentReconciler{Client: c, Scheme: s}
	res, err := r.Reconcile(context.TODO(), req())
	if err != nil || res != (ctrl.Result{}) {
		t.Fatalf("res %+v err %v", res, err)
	}
}

// Удаление: cleanup удаляет дочерние Trial и PVC, затем снимает finalizer.
func TestReconcileDeleteCleanup(t *testing.T) {
	s := newScheme(t)
	exp := baseExp()
	exp.Finalizers = []string{experimentFinalizer}
	now := metav1.Now()
	exp.DeletionTimestamp = &now
	trial := &mlv1.Trial{
		ObjectMeta: metav1.ObjectMeta{Name: "exp1-0", Namespace: "default",
			Labels: map[string]string{labelExperiment: "exp1"}},
		Spec: mlv1.TrialSpec{ExperimentRef: "exp1", Image: "trainer:v1"},
	}
	pvc := corev1PVC("exp1-0", "default")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(exp, trial, pvc).Build()
	r := &ExperimentReconciler{Client: c, Scheme: s}

	if _, err := r.Reconcile(context.TODO(), req()); err != nil {
		t.Fatal(err)
	}

	var tl mlv1.TrialList
	_ = c.List(context.TODO(), &tl)
	if len(tl.Items) != 0 {
		t.Fatalf("trials not deleted: %d", len(tl.Items))
	}
	var pl corev1PVCList
	_ = c.List(context.TODO(), &pl)
	for _, p := range pl.Items {
		if p.Name == "exp1-0" {
			t.Fatalf("trial pvc not deleted")
		}
	}

	var got mlv1.Experiment
	_ = c.Get(context.TODO(), types.NamespacedName{Name: "exp1", Namespace: "default"}, &got)
	for _, f := range got.Finalizers {
		if f == experimentFinalizer {
			t.Fatalf("finalizer still present")
		}
	}
}
