# source-watcher

[![test](https://github.com/stefanprodan/source-watcher/workflows/test/badge.svg)](https://github.com/stefanprodan/source-watcher/actions)

Example consumer of the GitOps Toolkit Source APIs.

![](https://raw.githubusercontent.com/fluxcd/toolkit/master/docs/_files/source-controller.png)

## Watch Git repositories

Source code:

* controller [gitrepository_watcher.go](controllers/gitrepository_watcher.go)
* predicate [gitrepository_predicate.go](controllers/gitrepository_predicate.go)

The `GitRepositoryWatcher` controller does the following:

* subscribes to `GitRepository` events
* detects when the Git revision changes
* downloads and extracts the source artifact
* write to stdout the extracted file names

### Prerequisites

* go >= 1.13
* kubebuilder >= 2.3
* kind >= 0.8

Clone source watcher repo:

```sh
git clone https://github.com/stefanprodan/source-watcher
cd source-watcher
```

Build the controller:

```sh
make
```

### Install the GitOps toolkit

Create a cluster for testing:

```sh
kind create cluster --name testing
```

Install the toolkit CLI:

```sh
curl -s https://toolkit.fluxcd.io/install.sh | sudo bash
```

Install the toolkit controllers:

```sh
tk install
```

### Run the controller

Port forward to source controller artifacts server:

```sh
kubectl -n gitops-system port-forward svc/source-controller 8080:80
```

Export the local address as `SOURCE_HOST`:

```sh
export SOURCE_HOST=localhost:8080
```

Run the controller:

```sh
make run
```

Create a Git source:

```sh
tk create source git test \
--url=https://github.com/stefanprodan/podinfo \
--tag=4.0.0
```

The source watcher should log the revision:

```console
New revision detected   {"gitrepository": "gitops-system/test", "revision": "4.0.0/ab953493ee14c3c9800bda0251e0c507f9741408"}
Extracted tarball into /var/folders/77/3y6x_p2j2g9fspdkzjbm5_s40000gn/T/test292235827: 123 files, 29 dirs (32.603415ms)
Processing files...
```

Change the git tag:

```sh
tk create source git test \
--url=https://github.com/stefanprodan/podinfo \
--tag=4.0.1
```

The source watcher should log the new revision:

```console
New revision detected   {"gitrepository": "gitops-system/test", "revision": "4.0.1/113360052b3153e439a0cf8de76b8e3d2a7bdf27"}
```
