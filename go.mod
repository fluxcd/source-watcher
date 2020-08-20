module github.com/stefanprodan/source-watcher

go 1.15

require (
	github.com/fluxcd/pkg/untar v0.0.5
	github.com/fluxcd/source-controller/api v0.0.9
	github.com/go-logr/logr v0.1.0
	go.uber.org/zap v1.13.0
	k8s.io/apimachinery v0.18.4
	k8s.io/client-go v0.18.4
	sigs.k8s.io/controller-runtime v0.6.1
)
