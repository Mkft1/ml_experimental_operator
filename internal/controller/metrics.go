// internal/controller/metrics.go — Prometheus-метрики контроллеров оператора.
package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// trialReconcileTotal — число проходов Reconcile контроллера Trial по фазам.
	trialReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trial_reconcile_total",
			Help: "Total number of Trial reconciles by phase.",
		},
		[]string{"phase"},
	)

	// trialDurationSeconds — длительность обучения trial (от старта до завершения Job).
	trialDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "trial_duration_seconds",
			Help:    "Duration of a Trial (training job) in seconds.",
			Buckets: prometheus.ExponentialBuckets(60, 4, 8), // ~1 мин .. ~2 сут
		},
	)

	// experimentTrialCreatedTotal — сколько Trial CR'ов создал контроллер Experiment
	// (по стратегии поиска и итоговому исходу эксперимента).
	experimentTrialCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "experiment_trial_created_total",
			Help: "Total number of Trial CRs created by the Experiment controller.",
		},
		[]string{"strategy"},
	)

	// experimentPhaseTransitionsTotal — переходы фаз экспериментов.
	experimentPhaseTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "experiment_phase_transitions_total",
			Help: "Total number of Experiment phase transitions.",
		},
		[]string{"phase"},
	)
)

func init() {
	// Регистрируем метрики в общий registry controller-runtime
	// (отдаётся на /metrics менеджера оператора).
	metrics.Registry.MustRegister(
		trialReconcileTotal,
		trialDurationSeconds,
		experimentTrialCreatedTotal,
		experimentPhaseTransitionsTotal,
	)
}
