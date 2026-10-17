// Package v1 содержит API-типы для группы ml.example.com (версия v1).
// +kubebuilder:object:generate=true
// +groupName=ml.example.com
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion — группа и версия API этого пакета.
	GroupVersion = schema.GroupVersion{Group: "ml.example.com", Version: "v1"}

	// SchemeBuilder используется для регистрации типов в схеме.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme добавляет типы этой группы в схему.
	AddToScheme = SchemeBuilder.AddToScheme
)
