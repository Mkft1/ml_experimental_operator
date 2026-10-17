// internal/search/search.go — общий контракт стратегий поиска гиперпараметров.
package search

import (
	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// Strategy — абстракция перебора пространства гиперпараметров.
// Конкретные реализации: grid (internal/search/grid.go) и random (random.go).
type Strategy interface {
	// Name возвращает имя стратегии ("grid" / "random").
	Name() string
	// Next отдаёт следующую неизрасходованную комбинацию гиперпараметров.
	// ok == false означает, что пространство поиска исчерпано.
	Next() (hyperparameters map[string]string, ok bool)
	// Peek возвращает i-ю комбинацию без расходования итератора.
	// ok == false, если индекс выходит за пределы пространства поиска.
	// Требуется контроллеру Experiment для детерминированного сопоставления
	// уже созданных Trial CR'ов с их слотами в пространстве поиска.
	Peek(i int) (hyperparameters map[string]string, ok bool)
	// Total — размер пространства поиска (с учётом лимита maxTrials).
	Total() int
}

// Factory — функция-конструктор стратегии для данного ExperimentSpec.
type Factory func(spec *mlv1.ExperimentSpec) (Strategy, error)

// Registry — реестр фабрик стратегий поиска по их имени.
// Контроллер Experiment использует его, чтобы построить стратегию
// для spec.search.strategy без прямой зависимости от конкретных реализаций.
type Registry struct {
	factories map[mlv1.SearchStrategy]Factory
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[mlv1.SearchStrategy]Factory)}
}

// Register добавляет фабрику стратегии под именем name.
// Повторная регистрация того же имени перезаписывает предыдущую.
func (r *Registry) Register(name mlv1.SearchStrategy, f Factory) {
	r.factories[name] = f
}

// Build конструирует стратегию для переданного спека эксперимента.
func (r *Registry) Build(spec *mlv1.ExperimentSpec) (Strategy, error) {
	f, ok := r.factories[spec.Search.Strategy]
	if !ok {
		return nil, ErrUnknownStrategy(spec.Search.Strategy)
	}
	return f(spec)
}

// ErrUnknownStrategy — стратегия не зарегистрирована в реестре.
type ErrUnknownStrategy mlv1.SearchStrategy

func (e ErrUnknownStrategy) Error() string {
	return "unknown search strategy: " + string(e)
}
