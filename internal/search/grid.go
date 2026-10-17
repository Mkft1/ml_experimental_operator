// internal/search/grid.go — стратегия GridSearch: полный перебор декартова
// произведения кандидатов гиперпараметров в детерминированном порядке.
package search

import (
	"fmt"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// SearchStrategy — контракт генератора пространства поиска
// (требование Шага 7). Принимает спеки эксперимента и возвращает
// плоский список комбинаций гиперпараметров.
// Реализации: *GridSearch (полное декартово произведение) и
// *RandomSearch (случайная выборка maxTrials комбинаций).
type SearchStrategy interface {
	// Generate строит список комбинаций по spec.search.parameters.
	// Возвращает ошибку, если параметры пусты/отсутствуют или задан nil spec.
	Generate(spec mlv1.ExperimentSpec) ([]map[string]string, error)
}

// GridSearch реализует SearchStrategy: декартово произведение всех значений
// из spec.search.parameters (например, lr={0.01,0.1} x dropout={0.2,0.5}
// даёт 4 комбинации). Порядок детерминирован: имена параметров сортируются,
// поэтому i-я комбинация одинакова при любом пересчёте — контроллер может
// однозначно сопоставлять Trial CR'ы с их слотами пространства поиска.
// Количество результатов ограничено spec.search.maxTrials.
type GridSearch struct{}

// Compile-time проверка соответствия интерфейсу.
var _ SearchStrategy = (*GridSearch)(nil)

// Generate возвращает все комбинации сетки. Ошибки:
//   - nil-экземпляр;
//   - пустые/отсутствующие Parameters (в т.ч. параметр со списком из 0 значений).
func (g *GridSearch) Generate(spec mlv1.ExperimentSpec) ([]map[string]string, error) {
	if g == nil {
		return nil, fmt.Errorf("grid search: method called on nil *GridSearch")
	}
	if len(spec.Search.Parameters) == 0 {
		return nil, fmt.Errorf("grid search: spec.search.parameters must not be empty")
	}
	for name, values := range spec.Search.Parameters {
		if len(values) == 0 {
			return nil, fmt.Errorf("grid search: parameter %q has no candidate values", name)
		}
	}
	combos := Combinations(&spec)
	if len(combos) == 0 {
		return nil, fmt.Errorf("grid search: empty search space generated from parameters")
	}
	return combos, nil
}

// NewGrid создаёт стратегию Strategy (итератор grid) для данного спека
// эксперимента через реестр фабрик. Используется контроллером Experiment
// (Registry.Build); низкоуровневая генерация — GridSearch.Generate.
func NewGrid(spec *mlv1.ExperimentSpec) (Strategy, error) {
	if spec == nil {
		return nil, fmt.Errorf("grid strategy: nil spec")
	}
	var gen SearchStrategy = &GridSearch{}
	combos, err := gen.Generate(*spec)
	if err != nil {
		return nil, err
	}
	return &gridIterator{combinations: combos}, nil
}

// gridIterator — последовательный доступ к комбинациям сетки
// (Next/Peek/Total), требуемый контроллеру Experiment.
type gridIterator struct {
	combinations []map[string]string
	cursor       int
}

func (g *gridIterator) Name() string { return string(mlv1.SearchStrategyGrid) }

func (g *gridIterator) Next() (map[string]string, bool) {
	if g.cursor >= len(g.combinations) {
		return nil, false
	}
	c := g.combinations[g.cursor]
	g.cursor++
	return c, true
}

func (g *gridIterator) Peek(i int) (map[string]string, bool) {
	if i < 0 || i >= len(g.combinations) {
		return nil, false
	}
	return g.combinations[i], true
}

func (g *gridIterator) Total() int { return len(g.combinations) }

// RegisterGrid добавляет фабрику grid-стратегии в реестр.
func RegisterGrid(r *Registry) {
	r.Register(mlv1.SearchStrategyGrid, NewGrid)
}
