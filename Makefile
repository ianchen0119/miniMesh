REGISTRY ?= minimesh
TAG      ?= latest

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/minimesh-daemon  ./cmd/daemon
	CGO_ENABLED=0 go build -o bin/meshctl          ./cmd/meshctl
	CGO_ENABLED=0 go build -o bin/minimesh-operator ./cmd/operator

.PHONY: docker-build
docker-build:
	docker build -t $(REGISTRY)/daemon:$(TAG)   -f Dockerfile.daemon   .
	docker build -t $(REGISTRY)/operator:$(TAG) -f Dockerfile.operator .

.PHONY: docker-push
docker-push:
	docker push $(REGISTRY)/daemon:$(TAG)
	docker push $(REGISTRY)/operator:$(TAG)

.PHONY: microk8s-deploy
microk8s-deploy:
	docker save $(REGISTRY)/daemon:$(TAG) -o daemon.tar
	docker save $(REGISTRY)/operator:$(TAG) -o operator.tar
	microk8s.ctr image import daemon.tar
	microk8s.ctr image import operator.tar
	rm daemon.tar operator.tar

.PHONY: rollout
rollout:
	kubectl rollout restart daemonset/minimesh-daemon -n kube-system
	kubectl rollout status daemonset/minimesh-daemon -n kube-system
	kubectl rollout restart deployment/minimesh-operator -n kube-system
	kubectl rollout status deployment/minimesh-operator -n kube-system

.PHONY: deploy
deploy:
	kubectl apply -f deploy/crds/crds.yaml
	kubectl apply -f deploy/daemon.yaml
	kubectl apply -f deploy/operator.yaml

.PHONY: undeploy
undeploy:
	kubectl delete -f deploy/operator.yaml --ignore-not-found
	kubectl delete -f deploy/daemon.yaml   --ignore-not-found
	kubectl delete -f deploy/crds/crds.yaml --ignore-not-found

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: lint
lint:
	go vet ./...

.PHONY: test
test:
	go test ./...
