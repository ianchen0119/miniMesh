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
