# 相关概念说明

- **Spec**：声明期望的内容（期望状态），即你希望资源最终达到的配置。例如副本数、镜像名等。
- **Status**：记录运行时的实际数据（实际状态），如当前已分配的 id、实际运行的 Pod 列表等，由控制器自动维护。
- **CRD（CustomResourceDefinition）**：自定义资源定义，类似于“类 class”，用于定义一种新的 K8s 资源类型及其字段格式。
- **CR（CustomResource）**：自定义资源实例，类似于“对象 object”，是 CRD 的具体实例，包含实际的 spec 和 status。

# 练习说明与主要文件作用

## 主要修改文件及作用
- 每次修改 `api/v1/boxcontroller_types.go`（如新增字段、调整结构体）后，需运行 `make generate` 或 `make manifests`，以自动生成/更新 CRD 校验文件和 deepcopy 代码，保证 CRD 与代码结构一致。

- `api/v1/boxcontroller_types.go`：定义 BoxController CRD 的 Spec/Status 结构体，决定 CRD 支持的字段和状态。
- `internal/controller/boxcontroller_controller.go`：核心控制器逻辑，实现副本自动补齐/删除、唯一 id 分配、Pod 管理等。
- `config/crd/bases/core.zhengzhihua2017.com_boxcontrollers.yaml`：自动生成的 CRD 校验 schema，决定 CRD 在 K8s 中的注册和校验规则。
- `config/samples/core_v1_boxcontroller.yaml`：BoxController CR 示例，便于本地测试和演示。
- `Makefile`：定义常用开发、构建、部署命令。

## 本地调试运行步骤

1. 确保本地已安装好 Go、kubectl、Kubernetes 集群（如 minikube/kind）并配置好 KUBECONFIG。
2. 安装 CRD：
	```sh
	make install
	# 或 kubectl apply -f config/crd/bases
	```
3. 本地运行控制器（连接当前集群）：
	```sh
	make run
	# 或 go run ./main.go
	```
4. 应用 CR 示例，观察控制器效果：
	```sh
	kubectl apply -f config/samples/core_v1_boxcontroller.yaml
	kubectl get pods
	kubectl get boxcontrollers.core.zhengzhihua2017.com -o yaml
	```

## 正式部署运行步骤（以 default 命名空间 + 本地 Docker Desktop 为例）

### 1. 确认命名空间与 ServiceAccount

- 本项目最终选择在 `default` 命名空间里运行 controller：
	- `config/manager/manager.yaml` 中：
		- `Namespace` 资源的 `metadata.name` 为 `default`（或删除该 Namespace 对象，直接用已有的 `default`）。
		- `Deployment` 的 `metadata.namespace` 为 `default`。
	- `config/rbac/service_account.yaml` 中：
		- `metadata.namespace: default`。
	- `config/rbac/role_binding.yaml` 中：
		- `subjects.namespace: default`，`subjects.name: box-controller-controller-manager`（与 Deployment 里使用的 ServiceAccount 名字一致）。

### 2. 本地构建 controller 镜像（Docker Desktop 环境）

在 `box-controller` 项目根目录：

```sh
make docker-build IMG=controller:latest
```

构建完成后可用：

```sh
docker images | grep controller
```

确认存在 `controller:latest` 镜像。

### 3. 配置 manager 使用本地镜像

- `config/manager/kustomization.yaml`：

```yaml
images:
- name: controller
	newName: controller
	newTag: latest
```

- `config/manager/manager.yaml` 中容器配置：

```yaml
containers:
- image: controller:latest
	imagePullPolicy: IfNotPresent
	name: manager
	# ...existing code...
```

`IfNotPresent` 可以避免每次都去远程拉镜像，优先使用本地 Docker Desktop 里的镜像。

### 4. 安装 CRD

```sh
make install
```

确认 CRD 已安装：

```sh
kubectl get crd | grep boxcontrollers
```

### 5. 配置 RBAC（允许 controller 管理 BoxController 和 Pods）

`config/rbac/role.yaml` 中的 `ClusterRole`（`manager-role`）包含：

- 对自定义资源 `boxcontrollers.core.zhengzhihua2017.com` 的完整权限：

```yaml
- apiGroups:
	- core.zhengzhihua2017.com
	resources:
	- boxcontrollers
	verbs:
	- create
	- delete
	- get
	- list
	- patch
	- update
	- watch
```

- 对 `boxcontrollers/status`、`boxcontrollers/finalizers` 的更新权限。

- 对集群内 `pods` 的管理权限：

```yaml
- apiGroups:
	- ""
	resources:
	- pods
	verbs:
	- get
	- list
	- watch
	- create
	- delete
	- update
	- patch
```

`config/rbac/role_binding.yaml` 中的 `ClusterRoleBinding`（`manager-rolebinding`）将上述 `ClusterRole` 绑定到 `default` 命名空间下的 ServiceAccount：

```yaml
kind: ClusterRoleBinding
metadata:
	name: manager-rolebinding
roleRef:
	apiGroup: rbac.authorization.k8s.io
	kind: ClusterRole
	name: manager-role
subjects:
- kind: ServiceAccount
	name: box-controller-controller-manager
	namespace: default
```

应用 RBAC：

```sh
kubectl apply -f config/rbac/ --validate=false
```

> 说明：`config/rbac/kustomization.yaml` 是给 kustomize 用的，不是 K8s 资源对象，用 `-f` 时会提示 `apiVersion not set, kind not set`，可以通过 `--validate=false` 忽略，或者使用 `kubectl apply -k config/rbac/`。

### 6. 部署 controller 到集群

```sh
make deploy
```

部署完成后检查 Pod：

```sh
kubectl get pods -n default
```

应看到形如：

```text
box-controller-controller-manager-xxxxxxx   1/1   Running   0   <age>
```

查看 controller 日志：

```sh
kubectl logs -l control-plane=controller-manager -n default
```

### 7. 应用 CR 示例并观察行为

```sh
kubectl apply -f config/samples/core_v1_boxcontroller.yaml
kubectl get boxcontroller -A
kubectl get pods -n default
```

根据 `BoxController` 的 `spec`（如 `replicas`）变化，观察 controller 自动创建/删除 Pod、分配 id，并在 `status` 中反映实际状态。

### 8. 常见问题排查小结

- **ErrImagePull / ImagePullBackOff**：
	- 检查本地是否已构建 `controller:latest`。
	- 确认 `config/manager/kustomization.yaml` 和 `manager.yaml` 中的镜像名一致。
	- 对 Docker Desktop，一般不需要远程仓库，本地镜像即可；设置 `imagePullPolicy: IfNotPresent`。

- **命名空间问题**：
	- 如果使用 `default` 命名空间，务必确保：
		- `Deployment.metadata.namespace = default`。
		- `ServiceAccount.metadata.namespace = default`。
		- `ClusterRoleBinding.subjects.namespace = default`。

- **RBAC 权限不足（cannot list/create pods/boxcontrollers）**：
	- 检查 `ClusterRole` 中是否包含对应资源和 `verbs`（例如 pods 的 `create` / `delete` / `get` / `list` / `watch` / `update` / `patch`）。
	- 确认 `ClusterRoleBinding` 绑定到了实际运行的 ServiceAccount（`system:serviceaccount:default:box-controller-controller-manager`）。

- **kustomization.yaml 报 apiVersion not set / kind not set**：
	- 属正常现象，`kustomization.yaml` 不是资源对象，使用 `-k` 或通过 `--validate=false` 忽略即可。

---
如需进一步了解每个文件的详细作用或调试技巧，可随时查阅注释或提问。
1. kubebuilder init --domain zhengzhihua2017.com --repo github.com/zhengzhihua2017/box-controller
2. kubebuilder create api --group core --version v1 --kind BoxController --namespaced=true