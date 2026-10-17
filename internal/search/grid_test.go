package search

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// gridSpec — spec эксперимента с сеткой гиперпараметров для тестов.
func gridSpec(params map[string][]string, maxTrials int32) mlv1.ExperimentSpec {
	return mlv1.ExperimentSpec{Search: mlv1.SearchConfig{
		Strategy:    mlv1.SearchStrategyGrid,
		Parameters:  params,
		MaxTrials:   maxTrials,
		MaxParallel: 1,
	}}
}

// comboKey — канонический вид комбинации для сравнения множеств
// ("dropout=0.2|lr=0.01"), независимый от порядка обхода мапы.
func comboKey(c map[string]string) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	// сортировка имён параметров
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("%s=%s|", k, c[k])
	}
	return out
}

// TestGridSearchCartesianCount — количество комбинаций равно декартову
// произведению размеров списков кандидатов (требование Шага 7, п.2).
func TestGridSearchCartesianCount(t *testing.T) {
	g := &GridSearch{}

	cases := []struct {
		name      string
		params    map[string][]string
		maxTrials int32
		wantLen   int
	}{
		{"2x2", map[string][]string{"lr": {"0.01", "0.1"}, "batch": {"32", "64"}}, 4, 4},
		{"2x2 limited by maxTrials", map[string][]string{"lr": {"0.01", "0.1"}, "batch": {"32", "64"}}, 3, 3},
		{"3x2x2 full", map[string][]string{"a": {"1", "2", "3"}, "b": {"x", "y"}, "c": {"p", "q"}}, 12, 12},
		{"single param", map[string][]string{"epochs": {"10", "20", "30"}}, 3, 3},
		{"space smaller than maxTrials", map[string][]string{"a": {"1", "2"}}, 100, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := gridSpec(tc.params, tc.maxTrials)
			combos, err := g.Generate(spec)
			if err != nil {
				t.Fatalf("Generate() unexpected error: %v", err)
			}
			if len(combos) != tc.wantLen {
				t.Fatalf("len(combos) = %d, want %d", len(combos), tc.wantLen)
			}
		})
	}
}

// TestGridSearchKeysCorrectness — каждая комбинация содержит ровно все ключи
// параметров, а значения берутся только из соответствующих списков кандидатов;
// все комбинации уникальны (требование Шага 7, п.2).
func TestGridSearchKeysCorrectness(t *testing.T) {
	params := map[string][]string{
		"lr":        {"0.001", "0.01", "0.1"},
		"dropout":   {"0.2", "0.5"},
		"optimizer": {"adam", "sgd"},
	}
	spec := gridSpec(params, 12) // 3*2*2 = 12
	combos, err := (&GridSearch{}).Generate(spec)
	if err != nil {
		t.Fatalf("Generate() unexpected error: %v", err)
	}

	seen := map[string]bool{}
	for i, c := range combos {
		if len(c) != len(params) {
			t.Fatalf("combo[%d]: got %d keys, want %d (%v)", i, len(c), len(params), c)
		}
		for name, values := range params {
			v, ok := c[name]
			if !ok {
				t.Fatalf("combo[%d]: missing key %q (%v)", i, name, c)
			}
			found := false
			for _, cand := range values {
				if v == cand {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("combo[%d]: value %q of %q not in candidates %v", i, v, name, values)
			}
		}
		key := comboKey(c)
		if seen[key] {
			t.Fatalf("combo[%d]: duplicate combination %v", i, c)
		}
		seen[key] = true
	}
	if len(seen) != 12 {
		t.Fatalf("unique combos = %d, want 12", len(seen))
	}
}

// TestGridSearchDeterministicOrder — порядок детерминирован (лексикографика
// по отсортированным именам параметров) и одинаков при повторных вызовах.
func TestGridSearchDeterministicOrder(t *testing.T) {
	spec := gridSpec(map[string][]string{"lr": {"0.01", "0.1"}, "dropout": {"0.2", "0.5"}}, 4)
	want := []map[string]string{
		{"dropout": "0.2", "lr": "0.01"},
		{"dropout": "0.2", "lr": "0.1"},
		{"dropout": "0.5", "lr": "0.01"},
		{"dropout": "0.5", "lr": "0.1"},
	}
	g := &GridSearch{}
	got, err := g.Generate(spec)
	if err != nil {
		t.Fatalf("Generate() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", got, want)
	}
	again, _ := g.Generate(spec)
	if !reflect.DeepEqual(again, got) {
		t.Fatal("Generate must be deterministic across calls")
	}
}

// TestGridSearchEmptyParametersError — пустые Parameters дают ошибку
// (требование Шага 7, п.4).
func TestGridSearchEmptyParametersError(t *testing.T) {
	cases := map[string]map[string][]string{
		"nil parameters":     nil,
		"empty map":          {},
		"param with no vals": {"lr": {}},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			spec := gridSpec(params, 4)
			combos, err := (&GridSearch{}).Generate(spec)
			if err == nil {
				t.Fatalf("expected error for %s, got combos=%v", name, combos)
			}
			if combos != nil {
				t.Fatalf("expected nil combos on error, got %v", combos)
			}
			t.Logf("error (as expected): %v", err)
		})
	}
}

// TestGridSearchImplementsInterface — GridSearch/RandomSearch удовлетворяют
// контракту SearchStrategy.
func TestGridSearchImplementsInterface(t *testing.T) {
	var g SearchStrategy = &GridSearch{}
	var r SearchStrategy = &RandomSearch{Rand: rand.New(rand.NewSource(1))}
	if _, err := g.Generate(gridSpec(map[string][]string{"a": {"1"}}, 1)); err != nil {
		t.Fatalf("GridSearch via interface: %v", err)
	}
	if _, err := r.Generate(gridSpec(map[string][]string{"a": {"1", "2"}}, 2)); err != nil {
		t.Fatalf("RandomSearch via interface: %v", err)
	}
}

// TestRandomSearchGeneratesMaxTrials — RandomSearch выдаёт ровно MaxTrials
// случайных комбинаций без повторений (требование Шага 7, п.3).
func TestRandomSearchGeneratesMaxTrials(t *testing.T) {
	params := map[string][]string{"a": {"1", "2", "3", "4"}, "b": {"x", "y"}} // пространство = 8
	spec := gridSpec(params, 5)
	combos, err := (&RandomSearch{Rand: rand.New(rand.NewSource(42))}).Generate(spec)
	if err != nil {
		t.Fatalf("Generate() unexpected error: %v", err)
	}
	if len(combos) != 5 {
		t.Fatalf("len(combos) = %d, want 5 (maxTrials)", len(combos))
	}
	seen := map[string]bool{}
	for _, c := range combos {
		if len(c) != 2 || c["a"] == "" || c["b"] == "" {
			t.Fatalf("bad combo %v: want keys a,b with non-empty values", c)
		}
		k := comboKey(c)
		if seen[k] {
			t.Fatalf("duplicate random combo %v", c)
		}
		seen[k] = true
	}
}

// TestRandomSearchEmptyParametersError — пустые Parameters дают ошибку.
func TestRandomSearchEmptyParametersError(t *testing.T) {
	spec := gridSpec(nil, 4)
	if _, err := (&RandomSearch{}).Generate(spec); err == nil {
		t.Fatal("expected error for empty parameters")
	}
}
