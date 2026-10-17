// internal/search/combinations.go — детерминированная генерация полного
// декартова произведения пространства гиперпараметров (вспомогательная
// функция для стратегий grid и random).
package search

import (
	"sort"

	mlv1 "github.com/myorg/ml-experiment-operator/api/v1"
)

// Combinations разворачивает spec.search.parameters в плоский список всех
// комбинаций гиперпараметров (декартово произведение списков кандидатов).
//
// Детерминизм: имена параметров сортируются по алфавиту, поэтому индекс i-й
// комбинации одинаков при любом пересчёте. Это позволяет контроллеру
// Experiment однозначно сопоставлять уже созданные Trial CR'ы с их слотами
// в пространстве поиска (trial имя = "<experiment>-<index>") и не создавать
// дубликаты при повторных reconcile-проходах.
//
// Ограничение: возвращается не более maxTrials комбинаций (spec.search.maxTrials).
// Пустой/nil parameters даёт пустой результат.
func Combinations(spec *mlv1.ExperimentSpec) []map[string]string {
	params := spec.Search.Parameters
	if len(params) == 0 {
		return nil
	}

	// Упорядоченные имена параметров — основа детерминированного перебора.
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Полное число комбинаций (произведение размеров списков кандидатов),
	// с защитой от переполнения.
	total := 1
	for _, k := range keys {
		n := len(params[k])
		if n == 0 {
			return nil // параметр без кандидатов — пространство пусто
		}
		total *= n
	}

	limit := int(spec.Search.MaxTrials) // 0 => лимит не применяется
	if limit <= 0 || total < limit {
		limit = total
	}

	// Итеративный перебор в лексикографическом порядке: keys[0] — самый
	// старший разряд (меняется медленнее), keys[len-1] — самый младший.
	// Порядок детерминирован и одинаков при любом пересчёте, поэтому
	// i-я комбинация всегда соответствует trial "<experiment>-<i>".
	out := make([]map[string]string, 0, limit)
	coords := make([]int, len(keys)) // индекс выбранного значения каждого параметра
	for {
		cp := make(map[string]string, len(keys))
		for d, key := range keys {
			cp[key] = params[key][coords[d]]
		}
		out = append(out, cp)
		if len(out) >= limit {
			return out // лимит maxTrials исчерпан
		}
		// Инкремент "многомерного счётчика" с переносом в старший разряд.
		carry := true
		for d := len(keys) - 1; d >= 0 && carry; d-- {
			coords[d]++
			if coords[d] < len(params[keys[d]]) {
				carry = false
			} else {
				coords[d] = 0
			}
		}
		if carry {
			return out // пространство исчерпано
		}
	}
}

// allCombinationsUnlimited — то же декартово произведение, что Combinations,
// но без ограничения maxTrials (нужно RandomSearch для равномерной выборки).
func allCombinationsUnlimited(spec *mlv1.ExperimentSpec) []map[string]string {
	s := *spec
	s.Search.MaxTrials = 0 // 0 => лимит не применяется (см. Combinations)
	return Combinations(&s)
}
