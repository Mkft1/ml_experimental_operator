// api/v1/trial_types.go — CRD-типы Trial (один запуск обучения с конкретным набором гиперпараметров).
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TrialPhase — фаза жизненного цикла trial.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type TrialPhase string

const (
	TrialPhasePending   TrialPhase = "Pending"
	TrialPhaseRunning   TrialPhase = "Running"
	TrialPhaseSucceeded TrialPhase = "Succeeded"
	TrialPhaseFailed    TrialPhase = "Failed"
)

// TrialSpec — желаемое состояние trial.
type TrialSpec struct {
	// ExperimentRef — имя родительского Experiment (владелец через ownerReference).
	// +kubebuilder:validation:Required
	ExperimentRef string `json:"experimentRef"`

	// Hyperparameters — конкретный набор гиперпараметров для этого запуска.
	// Пример: {"lr": "0.01", "dropout": "0.2"}
	// +optional
	Hyperparameters map[string]string `json:"hyperparameters,omitempty"`

	// Command — финальная команда запуска обучения (подставлена оператором из spec.experiment).
	// +optional
	Command []string `json:"command,omitempty"`

	// Image — контейнер обучения (наследуется из Experiment).
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Attempt — номер попытки запуска (растёт при ретраях, стартует с 1).
	// +kubebuilder:validation:Minimum=1
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
}

// TrialStatus — фактическое состояние trial.
type TrialStatus struct {
	// Phase — текущая фаза trial.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	// +optional
	Phase TrialPhase `json:"phase,omitempty"`

	// Metrics — собранные метрики (например {"val_loss": 0.42, "accuracy": 0.95}).
	// +optional
	Metrics map[string]float64 `json:"metrics,omitempty"`

	// StartTime — время начала обучения.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime — время завершения обучения.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// PodName — имя Pod, в котором выполнялось обучение.
	// +optional
	PodName string `json:"podName,omitempty"`

	// Message — человекочитаемое пояснение текущего состояния / причины отказа.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions — стандартные условия Kubernetes.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Trial — один запуск обучения внутри эксперимента.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tr
// +kubebuilder:printcolumn:name="Experiment",type=string,JSONPath=`.spec.experimentRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Attempt",type=integer,JSONPath=`.spec.attempt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Trial struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TrialSpec   `json:"spec,omitempty"`
	Status TrialStatus `json:"status,omitempty"`
}

// TrialList — список trials.
// +kubebuilder:object:root=true
type TrialList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Trial `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Trial{}, &TrialList{})
}
