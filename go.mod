module github.com/fluxcd/source-watcher

go 1.15

require (
	github.com/fluxcd/pkg/runtime v0.6.2
	github.com/fluxcd/pkg/untar v0.0.5
	github.com/fluxcd/source-controller/api v0.6.1
	github.com/go-logr/logr v0.3.0
	github.com/spf13/pflag v1.0.5
	k8s.io/apimachinery v0.19.4
	k8s.io/client-go v0.19.4
	sigs.k8s.io/controller-runtime v0.7.0
)
