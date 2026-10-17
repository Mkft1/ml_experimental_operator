package search

import (
	"reflect"
	"testing"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

func specWith(params map[string][]string, maxTrials int32) *mlv1.ExperimentSpec {
	return &mlv1.ExperimentSpec{Search: mlv1.SearchConfig{
		Strategy: mlv1.SearchStrategyGrid, Parameters: params, MaxTrials: maxTrials, MaxParallel: 1,
	}}
}

func TestCombinationsDeterministic(t *testing.T) {
	spec := specWith(map[string][]string{"lr": {"0.01", "0.1"}, "dropout": {"0.2", "0.5"}}, 4)
	want := []map[string]string{
		{"lr": "0.01", "dropout": "0.2"},
		{"lr": "0.01", "dropout": "0.5"},
		{"lr": "0.1", "dropout": "0.2"},
		{"lr": "0.1", "dropout": "0.5"},
	}
	got := Combinations(spec)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combos=%v want %v", got, want)
	}
	// Детерминизм: повторный вызов даёт тот же порядок.
	if !reflect.DeepEqual(Combinations(spec), want) {
		t.Fatal("Combinations must be deterministic")
	}
}

func TestCombinationsRespectsMaxTrials(t *testing.T) {
	spec := specWith(map[string][]string{"a": {"1", "2"}, "b": {"1", "2"}, "c": {"1", "2"}}, 5)
	if got := Combinations(spec); len(got) != 5 {
		t.Fatalf("len=%d want 5 (maxTrials limit)", len(got))
	}
}

func TestStrategiesNextAndPeek(t *testing.T) {
	for _, name := range []mlv1.SearchStrategy{mlv1.SearchStrategyGrid, mlv1.SearchStrategyRandom} {
		reg := DefaultRegistry()
		spec := specWith(map[string][]string{"x": {"1", "2", "3"}}, 3)
		spec.Search.Strategy = name
		s, err := reg.Build(spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Total() != 3 {
			t.Fatalf("%s: total=%d want 3", name, s.Total())
		}
		seen := map[string]bool{}
		for i := 0; ; i++ {
			c, ok := s.Next()
			if !ok {
				break
			}
			seen[c["x"]] = true
		}
		if len(seen) != 3 {
			t.Fatalf("%s: Next() produced duplicates/incomplete set: %v", name, seen)
		}
		if _, ok := s.Peek(2); !ok {
			t.Fatalf("%s: Peek(2) should succeed", name)
		}
		if _, ok := s.Peek(3); ok {
			t.Fatalf("%s: Peek(3) should fail", name)
		}
	}
}
