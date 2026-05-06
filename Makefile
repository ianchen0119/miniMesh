REGISTRY ?= minimesh
TAG      ?= latest

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/minimesh-daemon ./cmd/daemon
	CGO_ENABLED=0 go build -o bin/meshctl         ./cmd/meshctl

.PHONY: docker-build
docker-build:
	docker build -t $(REGISTRY)/daemon:$(TAG) -f Dockerfile.daemon .

.PHONY: docker-push
docker-push:
	docker push $(REGISTRY)/daemon:$(TAG)

.PHONY: microk8s-deploy
microk8s-deploy:
	docker save $(REGISTRY)/daemon:$(TAG) -o daemon.tar
	microk8s.ctr image import daemon.tar
	rm daemon.tar

.PHONY: rollout
rollout:
	kubectl rollout restart daemonset/minimesh-daemon -n kube-system
	kubectl rollout status daemonset/minimesh-daemon -n kube-system

.PHONY: deploy
deploy:
	kubectl apply -f deploy/daemon.yaml

.PHONY: undeploy
undeploy:
	kubectl delete -f deploy/daemon.yaml --ignore-not-found

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: lint
lint:
	go vet ./...

.PHONY: test
test:
	go test ./...
