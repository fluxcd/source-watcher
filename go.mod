module github.com/fluxcd/source-watcher

go 1.16

require (
	github.com/fluxcd/pkg/runtime v0.12.0
	github.com/fluxcd/pkg/untar v0.0.5
	github.com/fluxcd/source-controller/api v0.15.0
	github.com/go-logr/logr v0.4.0
	github.com/spf13/pflag v1.0.5
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v0.21.1
	sigs.k8s.io/controller-runtime v0.9.0
)
