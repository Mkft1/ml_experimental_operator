# ML Experiment Operator

Настоящий Kubernetes Operator на Go для запуска экспериментов по подбору
гиперпараметров машинного обучения (grid / random search) поверх Job'ов.

Управляет двумя CRD:

| CRD                | Group/Version        | Назначение                                             |
|--------------------|----------------------|--------------------------------------------------------|
| `Experiment`       | `ml.example.com/v1`  | Эксперимент: пространство поиска, ресурсы, лимиты      |
| `Trial`            | `ml.example.com/v1`  | Одна комбинация гиперпараметров = один Job             |

## Архитектура

```
                +--------------------------------------------+
                |              ml-experiment-operator         |
                |                                           |
 kubectl apply  |  ExperimentReconciler                     |
 Experiment --> |   * finalizer/cleanup (Trial PVC -> Trial)|
                |   * Pending: SearchRegistry.Build(strategy)|
                |       -> список комбинаций (grid/random)   |
                |   * Running: создать Trials до MaxParallel |
                |   * все завершены -> BestTrial/Succeeded   |
                |   * Failed > MaxRetries*Total -> Failed    |
                |            |                              |
                |            v                              |
                |   Trial CR  --->  TrialReconciler          |
                |                   * finalizer              |
                |                   * создать Job (ENV из    |
                |                     гиперпараметров)       |
                |                   * статус Job -> Phase    |
                +--------------------------------------------+
                        |                    |
                        v                    v
                 batch/v1 Job  ------>  Pod (обучение),
                 метрики Prometheus (:8080), healthz/readyz (:8081)
```

* `api/v1` — типы CRD (`Experiment`, `Trial`) с kubebuilder-валидацией и CEL-правилами.
* `internal/controller` — два контроллера (Experiment управляет Trial'ами, Trial управляет Job'ом).
* `internal/search` — стратегии `GridSearch` (декартово произведение) и `RandomSearch` (Fisher–Yates).
* `config/` — CRD, RBAC, манифесты деплоя, примеры.

## Установка

Предпосылки: Go >= 1.19, docker, kubectl, кластер Kubernetes >= 1.25
(CEL-валидация в CRD доступна по умолчанию с 1.25).

```bash
# 1. Сгенерировать deepcopy-код и манифесты (опционально, всё закоммичено)
make generate manifests

# 2. Сборка и тесты
make build test

# 3. Сборка и публикация образа
export IMG=registry.example.com/ml/experiment-operator:v0.1.0
make docker-build docker-push

# 4. Деплой в кластер
make deploy        # применяет config/crd, config/rbac, config/manager

# Локальная разработка против кластера:
make run
```

Проверка:

```bash
kubectl -n ml-system get pods -l control-plane=controller-manager
kubectl apply -f config/samples/ml_v1_experiment.yaml
kubectl get experiments -w
```

## Пример Experiment CR (grid search для ResNet)

См. [`config/samples/ml_v1_experiment.yaml`](config/samples/ml_v1_experiment.yaml):

```yaml
apiVersion: ml.example.com/v1
kind: Experiment
metadata:
  name: resnet-grid-search
spec:
  image: registry.example.com/ml/resnet-training:1.0.0
  command: ["python", "train.py"]
  dataset:
    claimName: imagenet-pvc
  search:
    strategy: grid
    parameters:
      lr: ["0.001", "0.01"]
      batch_size: ["32", "64"]   # 2 x 2 = 4 комбинации
    maxTrials: 4
    maxParallel: 2
  resources: {gpu: 1, cpu: "4", memory: 16Gi}
  earlyStopping: {metric: val_loss, patience: 5, minDelta: 0.001}
```

Метрики берутся из `Trial.Status.Metrics` (например `{"val_loss": 0.42}`):
контроллер считывает их из stdout Job'а по соглашению — последняя строка вывода
обучения должна быть JSON-объектом с метриками.

## Known Limitations

1. **EarlyStopping** описан в спецификации, но фактически не останавливает
   обучение внутри Job'а — параметра `patience/minDelta` пока не хватает в рантайме тренировки.
2. **Нет admission-webhook'ов**: валидация только декларативная (schema + CEL).
3. **PVC dataset/output должны существовать заранее** — оператор не провижинит хранилища.
4. **No autoscaling/retry on transient infra errors** — Failed trial засчитывается в бюджет `maxRetries`.
5. Random search выбирает комбинации без повторений из уже заданной сетки значений, а не из непрерывных распределений.
6. Один эксперимент = одно пространство параметров; многократные прогоны одной комбинации (seeds) не поддерживаются.
7. Метрики Prometheus имеют modest cardinality; алерты/дашборды не включены.
8. Leader election требует корректные RBAC для leases (добавьте при необходимости).
