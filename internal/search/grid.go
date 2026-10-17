// internal/search/grid.go — стратегия GridSearch: полный перебор декартова
// произведения кандидатов гиперпараметров в детерминированном порядке.
package search

import (
	"fmt"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// GridStrategy последовательно отдаёт все комбинации пространства поиска.
type GridStrategy struct {
	combinations []map[string]string
	cursor       int
}

// NewGrid создаёт стратегию grid для данного спека эксперимента.
func NewGrid(spec *mlv1.ExperimentSpec) (Strategy, error) {
	if spec == nil {
		return nil, fmt.Errorf("grid strategy: nil spec")
	}
	combos := Combinations(spec)
	if len(combos) == 0 {
		return nil, fmt.Errorf("grid strategy: empty search space (spec.search.parameters is required)")
	}
	return &GridStrategy{combinations: combos}, nil
}

func (g *GridStrategy) Name() string { return string(mlv1.SearchStrategyGrid) }

func (g *GridStrategy) Next() (map[string]string, bool) {
	if g.cursor >= len(g.combinations) {
		return nil, false
	}
	c := g.combinations[g.cursor]
	g.cursor++
	return c, true
}

func (g *GridStrategy) Peek(i int) (map[string]string, bool) {
	if i < 0 || i >= len(g.combinations) {
		return nil, false
	}
	return g.combinations[i], true
}

func (g *GridStrategy) Total() int { return len(g.combinations) }

// RegisterGrid добавляет фабрику grid-стратегии в реестр.
func RegisterGrid(r *Registry) {
	r.Register(mlv1.SearchStrategyGrid, NewGrid)
}
