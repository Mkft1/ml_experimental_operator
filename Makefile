# ML Experiment Operator - Makefile
IMG ?= registry.example.com/ml/experiment-operator:latest
CONTROLLER_GEN ?= $(shell command -v controller-gen 2>/dev/null || echo go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.11.4)
KUSTOMIZE ?= kustomize

.PHONY: all build run test docker-build docker-push manifests generate deploy undeploy fmt vet clean

all: build

## Build the operator binary -> bin/manager
build: fmt vet
	go build -o bin/manager cmd/main.go

## Run against the currently configured kubeconfig cluster locally
run: fmt vet
	go run ./cmd/main.go --metrics-bind-address=:8080 --health-probe-bind-address=:8081

## Run unit tests with race detector and coverage
test:
	go test ./... -coverprofile cover.out

## Build the operator container image
docker-build:
	docker build -t $(IMG) .

## Push the operator container image
docker-push:
	docker push $(IMG)

## Regenerate CRDs/RBAC manifests from kubebuilder markers
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=ml-experiment-operator paths="./..." \
		output:crd:artifacts:config=config/crd/bases

## Regenerate deepcopy code (zz_generated.deepcopy.go)
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..." || \
		$(CONTROLLER_GEN) object paths="./api/..."

## Deploy CRDs + RBAC + controller into the cluster
deploy: manifests
	kubectl apply -f config/crd/bases
	kubectl apply -f config/rbac
	kubectl apply -f config/manager

## Remove the operator from the cluster
undeploy:
	kubectl delete --ignore-not-found -f config/manager
	kubectl delete --ignore-not-found -f config/rbac
	kubectl delete --ignore-not-found -f config/crd/bases

fmt:
	gofmt -w ./api ./cmd ./internal

vet:
	go vet ./...

clean:
	rm -rf bin cover.out
