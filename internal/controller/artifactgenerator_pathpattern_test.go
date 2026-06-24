/*
Copyright 2025 The Flux authors

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

package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestBuildArtifactRequests(t *testing.T) {
	g := gomega.NewWithT(t)

	tmpDir, err := os.MkdirTemp("", "pattern-test")
	g.Expect(err).ToNot(gomega.HaveOccurred())
	defer os.RemoveAll(tmpDir)

	aliasDir := filepath.Join(tmpDir, "repo")
	err = os.MkdirAll(aliasDir, 0o755)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	localSources := map[string]string{
		"repo": aliasDir,
	}

	createFile := func(path string) {
		p := filepath.Join(aliasDir, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("test"), 0o644)
	}
	createDir := func(path string) {
		os.MkdirAll(filepath.Join(aliasDir, path), 0o755)
	}

	createDir("apps/auth/envs/dev")
	createDir("apps/auth/envs/prod")
	createDir("apps/payments")
	createFile("apps/ignore/envs/dev") // this is a file, not a dir

	t.Run("static path (no pathPattern)", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "static-1"},
					{Name: "static-2"},
				},
			},
		}
		reqs, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(reqs).To(gomega.HaveLen(2))
		g.Expect(reqs[0].Name).To(gomega.Equal("static-1"))
		g.Expect(reqs[0].Labels).To(gomega.BeNil())
	})

	t.Run("invalid pathPattern format", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "invalid-no-at-sign",
			},
		}
		_, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("invalid pathPattern format"))
	})

	t.Run("unknown alias in pattern", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@unknown/apps/{app}",
			},
		}
		_, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("not found in local sources"))
	})

	t.Run("invalid capture label key", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{_app}",
			},
		}
		_, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("capture variable"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("not a valid Kubernetes label key"))
	})

	t.Run("invalid capture syntax", func(t *testing.T) {
		cases := []struct {
			name       string
			pattern    string
			wantErrMsg string
		}{
			{name: "empty capture", pattern: "@repo/apps/{}", wantErrMsg: "empty capture variable"},
			{name: "unsupported capture name", pattern: "@repo/apps/{app-name}", wantErrMsg: "must match [A-Za-z0-9_]+"},
			{name: "missing closing brace", pattern: "@repo/apps/{app", wantErrMsg: "missing closing brace"},
			{name: "missing opening brace", pattern: "@repo/apps/app}", wantErrMsg: "missing opening brace"},
			{name: "duplicate capture", pattern: "@repo/apps/{app}/envs/{app}", wantErrMsg: "duplicate capture variable"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				g := gomega.NewWithT(t)
				obj := &swapi.ArtifactGenerator{
					Spec: swapi.ArtifactGeneratorSpec{
						PathPattern: tc.pattern,
					},
				}
				_, err := buildArtifactRequests(obj, localSources)
				g.Expect(err).To(gomega.HaveOccurred())
				g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
				g.Expect(err.Error()).To(gomega.ContainSubstring(tc.pattern))
				g.Expect(err.Error()).To(gomega.ContainSubstring(tc.wantErrMsg))
			})
		}
	})

	t.Run("single capture pattern", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{
						Name: "app-{{ .app }}",
						Copy: []swapi.CopyOperation{
							{From: "apps/{{ .app }}", To: "."},
						},
					},
				},
			},
		}
		reqs, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(reqs).To(gomega.HaveLen(3)) // auth, payments, and ignore

		names := []string{reqs[0].Name, reqs[1].Name, reqs[2].Name}
		g.Expect(names).To(gomega.ConsistOf("app-auth", "app-payments", "app-ignore"))

		for _, r := range reqs {
			if r.Name == "app-auth" {
				g.Expect(r.Labels).To(gomega.HaveKeyWithValue("app", "auth"))
				g.Expect(r.Copy[0].From).To(gomega.Equal("apps/auth"))
			}
		}
	})

	t.Run("preserves Exclude and Strategy fields", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{
						Name: "app-{{ .app }}",
						Copy: []swapi.CopyOperation{
							{
								From:     "apps/{{ .app }}",
								To:       ".",
								Exclude:  []string{"*.md", "tests/"},
								Strategy: "Merge",
							},
						},
					},
				},
			},
		}
		reqs, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(reqs).To(gomega.HaveLen(3))

		for _, r := range reqs {
			g.Expect(r.Copy[0].Exclude).To(gomega.Equal([]string{"*.md", "tests/"}))
			g.Expect(r.Copy[0].Strategy).To(gomega.Equal("Merge"))
		}
	})

	t.Run("skips files that match pattern exactly", func(t *testing.T) {
		g := gomega.NewWithT(t)
		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}/envs/{env}",
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "{{ .app }}-{{ .env }}"},
				},
			},
		}
		reqs, err := buildArtifactRequests(obj, localSources)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(reqs).To(gomega.HaveLen(2)) // only auth/envs/dev and auth/envs/prod, ignore is a file

		names := []string{reqs[0].Name, reqs[1].Name}
		g.Expect(names).To(gomega.ConsistOf("auth-dev", "auth-prod"))

		for _, r := range reqs {
			g.Expect(r.Labels["app"]).To(gomega.Equal("auth"))
			g.Expect(r.Labels["env"]).To(gomega.BeElementOf("dev", "prod"))
		}
	})

	t.Run("uppercase directories are lowercased", func(t *testing.T) {
		g := gomega.NewWithT(t)

		upperDir, err := os.MkdirTemp("", "upper-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(upperDir)

		upperAliasDir := filepath.Join(upperDir, "repo")
		os.MkdirAll(filepath.Join(upperAliasDir, "apps", "Auth", "envs", "Dev"), 0o755)
		os.MkdirAll(filepath.Join(upperAliasDir, "apps", "Payments", "envs", "Prod"), 0o755)

		upperSources := map[string]string{"repo": upperAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}/envs/{env}",
				OutputArtifacts: []swapi.OutputArtifact{
					{
						Name: "{{ .app }}-{{ .env }}",
						Copy: []swapi.CopyOperation{
							{From: "@repo/apps/{{ .app }}/envs/{{ .env }}/**", To: "@artifact/{{ .app }}/{{ .env }}/"},
						},
					},
				},
			},
		}
		reqs, err := buildArtifactRequests(obj, upperSources)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(reqs).To(gomega.HaveLen(2))

		names := []string{reqs[0].Name, reqs[1].Name}
		g.Expect(names).To(gomega.ConsistOf("auth-dev", "payments-prod"))

		for _, r := range reqs {
			// Labels should contain lowercased values.
			for _, v := range r.Labels {
				g.Expect(v).To(gomega.Equal(strings.ToLower(v)))
			}

			switch r.Name {
			case "auth-dev":
				g.Expect(r.Copy[0].From).To(gomega.Equal("@repo/apps/Auth/envs/Dev/**"))
				g.Expect(r.Copy[0].To).To(gomega.Equal("@artifact/Auth/Dev/"))
			case "payments-prod":
				g.Expect(r.Copy[0].From).To(gomega.Equal("@repo/apps/Payments/envs/Prod/**"))
				g.Expect(r.Copy[0].To).To(gomega.Equal("@artifact/Payments/Prod/"))
			}

		}
	})

	t.Run("invalid label value - leading dot", func(t *testing.T) {
		g := gomega.NewWithT(t)

		dotDir, err := os.MkdirTemp("", "dot-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(dotDir)

		dotAliasDir := filepath.Join(dotDir, "repo")
		os.MkdirAll(filepath.Join(dotAliasDir, "apps", ".hidden"), 0o755)

		dotSources := map[string]string{"repo": dotAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "app-{{ .app }}"},
				},
			},
		}
		_, err = buildArtifactRequests(obj, dotSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("not a valid Kubernetes label value"))
	})

	t.Run("invalid label value - contains space", func(t *testing.T) {
		g := gomega.NewWithT(t)

		spaceDir, err := os.MkdirTemp("", "space-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(spaceDir)

		spaceAliasDir := filepath.Join(spaceDir, "repo")
		os.MkdirAll(filepath.Join(spaceAliasDir, "apps", "my app"), 0o755)

		spaceSources := map[string]string{"repo": spaceAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "app-{{ .app }}"},
				},
			},
		}
		_, err = buildArtifactRequests(obj, spaceSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("not a valid Kubernetes label value"))
	})

	t.Run("invalid label value - too long", func(t *testing.T) {
		g := gomega.NewWithT(t)

		longDir, err := os.MkdirTemp("", "long-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(longDir)

		longAliasDir := filepath.Join(longDir, "repo")
		longName := strings.Repeat("a", 64) // 64 chars exceeds 63 char label value limit
		os.MkdirAll(filepath.Join(longAliasDir, "apps", longName), 0o755)

		longSources := map[string]string{"repo": longAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "app-{{ .app }}"},
				},
			},
		}
		_, err = buildArtifactRequests(obj, longSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("not a valid Kubernetes label value"))
	})

	t.Run("invalid rendered DNS name", func(t *testing.T) {
		g := gomega.NewWithT(t)

		dnsDir, err := os.MkdirTemp("", "dns-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(dnsDir)

		dnsAliasDir := filepath.Join(dnsDir, "repo")
		os.MkdirAll(filepath.Join(dnsAliasDir, "apps", "auth"), 0o755)

		dnsSources := map[string]string{"repo": dnsAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					// Template produces "auth..invalid" which is not a valid DNS subdomain.
					{Name: "{{ .app }}..invalid"},
				},
			},
		}
		_, err = buildArtifactRequests(obj, dnsSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("not a valid Kubernetes object name"))
	})

	t.Run("duplicate names after lowercasing", func(t *testing.T) {
		g := gomega.NewWithT(t)

		dupDir, err := os.MkdirTemp("", "dup-test")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		defer os.RemoveAll(dupDir)

		dupAliasDir := filepath.Join(dupDir, "repo")
		// Both will lowercase to "auth"
		os.MkdirAll(filepath.Join(dupAliasDir, "apps", "Auth"), 0o755)
		os.MkdirAll(filepath.Join(dupAliasDir, "apps", "auth"), 0o755)

		dupSources := map[string]string{"repo": dupAliasDir}

		obj := &swapi.ArtifactGenerator{
			Spec: swapi.ArtifactGeneratorSpec{
				PathPattern: "@repo/apps/{app}",
				OutputArtifacts: []swapi.OutputArtifact{
					{Name: "app-{{ .app }}"},
				},
			},
		}
		_, err = buildArtifactRequests(obj, dupSources)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("pathPattern"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("both resolve to artifact name"))
	})
}
