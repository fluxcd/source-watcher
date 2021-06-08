/*
Copyright 2020, 2021 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/untar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta1"
)

// GitRepositoryWatcher watches GitRepository objects for revision changes
type GitRepositoryWatcher struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories/status,verbs=get

func (r *GitRepositoryWatcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)

	// get source object
	var repository sourcev1.GitRepository
	if err := r.Get(ctx, req.NamespacedName, &repository); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("New revision detected", "revision", repository.Status.Artifact.Revision)

	// create tmp dir
	tmpDir, err := ioutil.TempDir("", repository.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create temp dir, error: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// download and extract artifact
	summary, err := r.fetchArtifact(ctx, repository, tmpDir)
	if err != nil {
		log.Error(err, "unable to fetch artifact")
		return ctrl.Result{}, err
	}
	log.Info(summary)

	// list artifact content
	files, err := ioutil.ReadDir(tmpDir)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list files, error: %w", err)
	}

	// do something with the artifact content
	for _, f := range files {
		log.Info("Processing " + f.Name())
	}

	return ctrl.Result{}, nil
}

func (r *GitRepositoryWatcher) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcev1.GitRepository{}, builder.WithPredicates(GitRepositoryRevisionChangePredicate{})).
		Complete(r)
}

func (r *GitRepositoryWatcher) fetchArtifact(ctx context.Context, repository sourcev1.GitRepository, dir string) (string, error) {
	if repository.Status.Artifact == nil {
		return "", fmt.Errorf("respository %s does not containt an artifact", repository.Name)
	}

	url := repository.Status.Artifact.URL

	// for local run:
	// kubectl -n flux-system port-forward svc/source-controller 8080:80
	// export SOURCE_HOST=localhost:8080
	if hostname := os.Getenv("SOURCE_HOST"); hostname != "" {
		url = fmt.Sprintf("http://%s/gitrepository/%s/%s/latest.tar.gz", hostname, repository.Namespace, repository.Name)
	}

	// download the tarball
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request, error: %w", err)
	}

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to download artifact from %s, error: %w", url, err)
	}
	defer resp.Body.Close()

	// check response
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download artifact, status: %s", resp.Status)
	}

	// extract
	summary, err := untar.Untar(resp.Body, dir)
	if err != nil {
		return "", fmt.Errorf("faild to untar artifact, error: %w", err)
	}

	return summary, nil
}
