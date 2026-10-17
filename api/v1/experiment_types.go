// api/v1/experiment_types.go — CRD-типы Experiment (ML-эксперименты: поиск гиперпараметров).
package v1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SearchStrategy — способ перебора гиперпараметров.
// +kubebuilder:validation:Enum=grid;random
type SearchStrategy string

const (
	SearchStrategyGrid   SearchStrategy = "grid"
	SearchStrategyRandom SearchStrategy = "random"
)

// ExperimentPhase — фаза жизненного цикла эксперимента.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type ExperimentPhase string

const (
	ExperimentPhasePending   ExperimentPhase = "Pending"
	ExperimentPhaseRunning   ExperimentPhase = "Running"
	ExperimentPhaseSucceeded ExperimentPhase = "Succeeded"
	ExperimentPhaseFailed    ExperimentPhase = "Failed"
)

// PVCSource описывает подключение существующего PersistentVolumeClaim.
type PVCSource struct {
	// ClaimName — имя существующего PVC.
	// +kubebuilder:validation:Required
	ClaimName string `json:"claimName"`
}

// SearchConfig конфигурирует стратегию поиска гиперпараметров.
type SearchConfig struct {
	// Strategy — стратегия поиска: grid или random.
	// +kubebuilder:validation:Required
	Strategy SearchStrategy `json:"strategy"`

	// Parameters — пространство поиска: имя гиперпараметра -> список кандидатов.
	// Пример: {"lr": ["0.01","0.1"], "dropout": ["0.2","0.5"]}
	// +optional
	Parameters map[string][]string `json:"parameters,omitempty"`

	// MaxTrials — максимальное число trials в эксперименте.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxTrials int32 `json:"maxTrials"`

	// MaxParallel — максимум одновременно запущенных trials.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxParallel int32 `json:"maxParallel"`
}

// ResourceRequirements — ресурсы на один trial (GPU/CPU/Memory).
type ResourceRequirements struct {
	// GPU — количество GPU (целое, например 1).
	// +optional
	GPU *resource.Quantity `json:"gpu,omitempty"`

	// CPU — запрос/лимит CPU (например "2" или "500m").
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// Memory — запрос/лимит памяти (например "4Gi").
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`
}

// OutputConfig — куда складывать артефакты и метрики trials.
type OutputConfig struct {
	// PVC — claim для артефактов.
	// +kubebuilder:validation:Required
	PVC PVCSource `json:"pvc"`

	// Path — путь внутри смонтированного PVC (например "/output").
	// +kubebuilder:validation:Required
	Path string `json:"path"`
}

// EarlyStoppingConfig — остановка trial по плато метрики.
type EarlyStoppingConfig struct {
	// Metric — имя отслеживаемой метрики (например "val_loss").
	// +kubebuilder:validation:Required
	Metric string `json:"metric"`

	// Patience — сколько эпох без улучшения терпим.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Patience int32 `json:"patience,omitempty"`

	// MinDelta — минимальная положительная дельта, считаемая улучшением.
	// +optional
	MinDelta string `json:"minDelta,omitempty"`
}

// ExperimentSpec — желаемое состояние эксперимента.
// +kubebuilder:validation:XValidation:rule="self.maxParallel <= self.maxTrials",message="maxParallel must be <= maxTrials"
type ExperimentSpec struct {
	// Image — контейнер с обучением (например "myorg/ml-trainer:v1").
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Command — команда запуска обучения. Аргументы гиперпараметров подставляет оператор.
	// +optional
	Command []string `json:"command,omitempty"`

	// Dataset — PVC с датасетом (монтируется read-only).
	// +kubebuilder:validation:Required
	Dataset PVCSource `json:"dataset"`

	// Search — конфигурация поиска гиперпараметров.
	// +kubebuilder:validation:Required
	Search SearchConfig `json:"search"`

	// Resources — ресурсы на каждый trial.
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// Output — PVC и путь для артефактов/метрик.
	// +optional
	Output OutputConfig `json:"output,omitempty"`

	// MaxRetries — сколько раз перезапускать упавший trial.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// EarlyStopping — ранняя остановка trials (опционально).
	// +optional
	EarlyStopping *EarlyStoppingConfig `json:"earlyStopping,omitempty"`

	// Priority — приоритет планирования экспериментов (больше = важнее).
	// +kubebuilder:validation:Minimum=0
	// +optional
	Priority int32 `json:"priority,omitempty"`
}

// TrialCounters — агрегированные счётчики trials.
type TrialCounters struct {
	// Total — всего созданных trials.
	Total int32 `json:"total"`
	// Pending — ожидающие запуска trials.
	Pending int32 `json:"pending"`
	// Running — запущенные trials.
	Running int32 `json:"running"`
	// Succeeded — успешно завершённые trials.
	Succeeded int32 `json:"succeeded"`
	// Failed — неуспешно завершённые trials.
	Failed int32 `json:"failed"`
}

// BestTrialInfo — информация о лучшем trial по целевой метрике.
type BestTrialInfo struct {
	// Name — имя лучшего Trial.
	// +optional
	Name string `json:"name,omitempty"`

	// Metrics — значения метрик лучшего trial.
	// +optional
	Metrics map[string]float64 `json:"metrics,omitempty"`

	// Hyperparameters — гиперпараметры лучшего trial.
	// +optional
	Hyperparameters map[string]string `json:"hyperparameters,omitempty"`
}

// ExperimentStatus — фактическое состояние эксперимента.
type ExperimentStatus struct {
	// Phase — текущая фаза эксперимента.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	// +optional
	Phase ExperimentPhase `json:"phase,omitempty"`

	// Trials — счётчики trials по состояниям.
	// +optional
	Trials TrialCounters `json:"trials,omitempty"`

	// BestTrial — лучший trial эксперимента.
	// +optional
	BestTrial BestTrialInfo `json:"bestTrial,omitempty"`

	// Conditions — стандартные условия Kubernetes.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Experiment — ML-эксперимент с поиском гиперпараметров.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=exp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.search.strategy`
// +kubebuilder:printcolumn:name="Trials",type=integer,JSONPath=`.status.trials.total`
// +kubebuilder:printcolumn:name="Running",type=integer,JSONPath=`.status.trials.running`
// +kubebuilder:printcolumn:name="Succeeded",type=integer,JSONPath=`.status.trials.succeeded`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.trials.failed`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Experiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExperimentSpec   `json:"spec,omitempty"`
	Status ExperimentStatus `json:"status,omitempty"`
}

// ExperimentList — список экспериментов.
// +kubebuilder:object:root=true
type ExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Experiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Experiment{}, &ExperimentList{})
}
