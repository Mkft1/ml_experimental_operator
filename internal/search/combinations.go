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
	// с защитой от переполнения int32.
	total := 1
	for _, k := range keys {
		n := len(params[k])
		if n == 0 {
			return nil // параметр без кандидатов — пространство пусто
		}
		if total > int(spec.Search.MaxTrials)/n {
			total = int(spec.Search.MaxTrials) + 1 // достаточно знать, что "много"
			break
		}
		total *= n
	}

	limit := int(spec.Search.MaxTrials)
	if limit <= 0 || total < limit {
		limit = total
	}

	out := make([]map[string]string, 0, limit)
	cur := make(map[string]string, len(keys))
	var rec func(depth int)
	rec = func(depth int) {
		if depth == len(keys) {
			cp := make(map[string]string, len(cur))
			for k, v := range cur {
				cp[k] = v
			}
			out = append(out, cp)
			return
		}
		key := keys[depth]
		for _, val := range params[key] {
			if len(out) >= limit {
				return // лимит maxTrials исчерпан
			}
			cur[key] = val
			rec(depth + 1)
		}
	}
	rec(0)
	return out
}
