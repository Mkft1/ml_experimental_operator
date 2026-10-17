// internal/search/random.go — стратегия RandomSearch: случайная выборка
// комбинаций гиперпараметров из пространства поиска.
package search

import (
	"fmt"
	"math/rand"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// RandomSearch реализует SearchStrategy: генерирует ровно spec.search.maxTrials
// случайных комбинаций (выборка без возвратов из полного декартова произведения
// кандидатов). Если пространство поиска меньше maxTrials, возвращаются все
// возможные комбинации в случайном порядке. Повторы значений одного параметра
// внутри одной комбинации исключены структурой пространства.
type RandomSearch struct {
	// Rand — источник случайности; nil означает math/rand глобальный источник.
	// Позволяет injecting детеменированного rand.New(rand.NewSource(seed)) в тестах.
	Rand *rand.Rand
}

// Compile-time проверка соответствия интерфейсу.
var _ SearchStrategy = (*RandomSearch)(nil)

// Generate строит список случайных комбинаций. Ошибки:
//   - nil-экземпляр;
//   - пустые/отсутствующие Parameters (требование Шага 7, п.4);
//   - параметр со списком из 0 значений.
func (r *RandomSearch) Generate(spec mlv1.ExperimentSpec) ([]map[string]string, error) {
	if r == nil {
		return nil, fmt.Errorf("random search: method called on nil *RandomSearch")
	}
	if len(spec.Search.Parameters) == 0 {
		return nil, fmt.Errorf("random search: spec.search.parameters must not be empty")
	}
	for name, values := range spec.Search.Parameters {
		if len(values) == 0 {
			return nil, fmt.Errorf("random search: parameter %q has no candidate values", name)
		}
	}

	// Полное пространство без ограничения maxTrials: имена параметров
	// сортируются (см. Combinations), поэтому перемешивание даёт корректную
	// равномерную выборку без повторений.
	full := allCombinationsUnlimited(&spec)
	if len(full) == 0 {
		return nil, fmt.Errorf("random search: empty search space generated from parameters")
	}

	want := int(spec.Search.MaxTrials)
	if want <= 0 || want > len(full) {
		want = len(full) // maxTrials не задан или больше пространства
	}

	// Частичное Fisher-Yates: первые want элементов перемешанного среза —
	// равновероятная выборка без возвратов.
	src := r.Rand
	if src == nil {
		src = rand.New(rand.NewSource(rand.Int63()))
	}
	for i := 0; i < want; i++ {
		j := i + src.Intn(len(full)-i)
		full[i], full[j] = full[j], full[i]
	}
	return full[:want], nil
}

// NewRandom создаёт стратегию Strategy (итератор random) для данного спека
// эксперимента через реестр фабрик. Используется контроллером Experiment
// (Registry.Build); низкоуровневая генерация — RandomSearch.Generate.
//
// Порядок комбинаций фиксируется один раз при построении стратегии: Peek(i)
// остаётся стабильным на время жизни итератора, что нужно контроллеру для
// сопоставления Trial CR'ов со слотами пространства поиска.
func NewRandom(spec *mlv1.ExperimentSpec) (Strategy, error) {
	if spec == nil {
		return nil, fmt.Errorf("random strategy: nil spec")
	}
	var gen SearchStrategy = &RandomSearch{}
	combos, err := gen.Generate(*spec)
	if err != nil {
		return nil, err
	}
	return &randomIterator{combinations: combos}, nil
}

// randomIterator — последовательный доступ к выбранной случайной подпоследо-
// вательности комбинаций (Next/Peek/Total).
type randomIterator struct {
	combinations []map[string]string
	cursor       int
}

func (rs *randomIterator) Name() string { return string(mlv1.SearchStrategyRandom) }

func (rs *randomIterator) Next() (map[string]string, bool) {
	if rs.cursor >= len(rs.combinations) {
		return nil, false
	}
	c := rs.combinations[rs.cursor]
	rs.cursor++
	return c, true
}

func (rs *randomIterator) Peek(i int) (map[string]string, bool) {
	if i < 0 || i >= len(rs.combinations) {
		return nil, false
	}
	return rs.combinations[i], true
}

func (rs *randomIterator) Total() int { return len(rs.combinations) }

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
