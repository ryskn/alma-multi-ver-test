GOPATH_BIN := $(shell go env GOPATH)/bin
export PATH := $(GOPATH_BIN):$(PATH)

.PHONY: proto agent controller build up down ping exec run clean

build: agent controller

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/alma.proto

agent: proto
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o agent/alma-agent ./agent

controller: proto
	go build -o controller/alma-ctl ./controller

up: agent
	vagrant up

down:
	vagrant halt

ping: controller
	./controller/alma-ctl ping

exec: controller
	@test -n "$(S)" || (echo "usage: make exec S=scripts/example.sh" && exit 1)
	./controller/alma-ctl exec $(S)

run: controller
	@test -n "$(J)" || (echo "usage: make run J=jobs/example.yaml" && exit 1)
	./controller/alma-ctl run $(J)

VL_SRC := ../vagrant-libvirt
VL_BRANCHES := alma8:el8 alma9:el9 alma10:el10

build-vl:
	@for pair in $(VL_BRANCHES); do \
		vm=$${pair%%:*}; branch=$${pair##*:}; \
		echo "=== Preparing $$vm (branch $$branch) ==="; \
		(cd $(VL_SRC) && git archive --prefix=vagrant-libvirt-src/ $$branch) | \
			vagrant ssh $$vm -c "sudo rm -rf /tmp/vagrant-libvirt-src && sudo tar xf - -C /tmp"; \
		cat scripts/build-vagrant-libvirt.sh | \
			vagrant ssh $$vm -c "cat > /tmp/build-vl.sh && sudo bash /tmp/build-vl.sh"; \
	done

clean:
	rm -f agent/alma-agent controller/alma-ctl
	rm -f proto/*.pb.go
