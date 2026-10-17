// internal/search/random.go — стратегия RandomSearch: случайная выборка
// комбинаций без повторений из пространства гиперпараметров.
package search

import (
	"fmt"
	"math/rand"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// RandomStrategy выдаёт случайную перестановку всех комбинаций пространства
// поиска (выборка без возвратов). Перемешивание выполняется один раз при
// построении стратегии; Peek/Next работают по уже перемешанному срезу.
type RandomStrategy struct {
	combinations []map[string]string
	cursor       int
}

// NewRandom создаёт стратегию random для данного спека эксперимента.
func NewRandom(spec *mlv1.ExperimentSpec) (Strategy, error) {
	if spec == nil {
		return nil, fmt.Errorf("random strategy: nil spec")
	}
	combos := Combinations(spec)
	if len(combos) == 0 {
		return nil, fmt.Errorf("random strategy: empty search space (spec.search.parameters is required)")
	}
	perm := rand.New(rand.NewSource(rand.Int63())).Perm(len(combos))
	shuffled := make([]map[string]string, len(combos))
	for i, p := range perm {
		shuffled[i] = combos[p]
	}
	return &RandomStrategy{combinations: shuffled}, nil
}

func (rs *RandomStrategy) Name() string { return string(mlv1.SearchStrategyRandom) }

func (rs *RandomStrategy) Next() (map[string]string, bool) {
	if rs.cursor >= len(rs.combinations) {
		return nil, false
	}
	c := rs.combinations[rs.cursor]
	rs.cursor++
	return c, true
}

func (rs *RandomStrategy) Peek(i int) (map[string]string, bool) {
	if i < 0 || i >= len(rs.combinations) {
		return nil, false
	}
	return rs.combinations[i], true
}

func (rs *RandomStrategy) Total() int { return len(rs.combinations) }

// RegisterRandom добавляет фабрику random-стратегии в реестр.
func RegisterRandom(r *Registry) {
	r.Register(mlv1.SearchStrategyRandom, NewRandom)
}

// DefaultRegistry — реестр со встроенными стратегиями grid и random.
// Используется в cmd/main.go при старте менеджера.
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	RegisterGrid(reg)
	RegisterRandom(reg)
	return reg
}
