package v1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestExperimentDeepCopyRoundTrip(t *testing.T) {
	in := &Experiment{
		ObjectMeta: metav1.ObjectMeta{Name: "exp1", Namespace: "default"},
		Spec: ExperimentSpec{
			Image:   "trainer:v1",
			Command: []string{"python", "train.py"},
			Dataset: PVCSource{ClaimName: "ds"},
			Search: SearchConfig{
				Strategy:   SearchStrategyGrid,
				Parameters: map[string][]string{"lr": {"0.01", "0.1"}},
				MaxTrials:  4, MaxParallel: 2,
			},
			EarlyStopping: &EarlyStoppingConfig{Metric: "val_loss", Patience: 3},
		},
		Status: ExperimentStatus{
			Phase: ExperimentPhaseRunning,
			BestTrial: BestTrialInfo{
				Name:            "exp1-0",
				Metrics:         map[string]float64{"val_loss": 0.42},
				Hyperparameters: map[string]string{"lr": "0.01"},
			},
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
		},
	}
	out := in.DeepCopy()
	// Мутируем копию — оригинал не должен измениться.
	out.Spec.Search.Parameters["lr"][0] = "MUTATED"
	out.Spec.Command[0] = "MUTATED"
	out.Status.BestTrial.Metrics["val_loss"] = 999
	out.Status.Conditions[0].Type = "MUTATED"
	if in.Spec.Search.Parameters["lr"][0] != "0.01" {
		t.Fatal("map[string][]string shared between copies!")
	}
	if in.Spec.Command[0] != "python" {
		t.Fatal("slice shared!")
	}
	if in.Status.BestTrial.Metrics["val_loss"] != 0.42 {
		t.Fatal("metrics map shared!")
	}
	if in.Status.Conditions[0].Type != "Ready" {
		t.Fatal("conditions slice shared!")
	}
	// runtime.Object интерфейс
	var _ runtime.Object = out
	var obj runtime.Object = in.DeepCopyObject()
	if _, ok := obj.(*Experiment); !ok {
		t.Fatalf("DeepCopyObject returned %T", obj)
	}
}

func TestListDeepCopyObject(t *testing.T) {
	list := &ExperimentList{Items: []Experiment{{Spec: ExperimentSpec{Image: "a"}}}}
	obj := list.DeepCopyObject()
	clone, ok := obj.(*ExperimentList)
	if !ok {
		t.Fatalf("ExperimentList.DeepCopyObject returned %T, want *ExperimentList", obj)
	}
	clone.Items[0].Spec.Image = "b"
	if list.Items[0].Spec.Image != "a" {
		t.Fatal("Items slice shared!")
	}
	tlist := &TrialList{Items: []Trial{{Spec: TrialSpec{Hyperparameters: map[string]string{"lr": "0.1"}}}}}
	tobj := tlist.DeepCopyObject()
	tclone, ok := tobj.(*TrialList)
	if !ok {
		t.Fatalf("TrialList.DeepCopyObject returned %T", tobj)
	}
	tclone.Items[0].Spec.Hyperparameters["lr"] = "MUT"
	if tlist.Items[0].Spec.Hyperparameters["lr"] != "0.1" {
		t.Fatal("Trial hyperparameters shared!")
	}
}
