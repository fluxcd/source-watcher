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

package builder

import (
	"errors"
	"fmt"
	"hash/adler32"
	"io"
	"slices"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"
)

var builderHash dirhash.Hash = DirHash

// DirHash computes a hash of the given files contents using Adler-32.
func DirHash(files []string, open func(string) (io.ReadCloser, error)) (string, error) {
	h := adler32.New()
	files = append([]string(nil), files...)
	slices.Sort(files)
	for _, file := range files {
		if strings.Contains(file, "\n") {
			return "", errors.New("filenames with newlines are not supported")
		}
		r, err := open(file)
		if err != nil {
			return "", err
		}
		hf := adler32.New()
		_, err = io.Copy(hf, r)
		r.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%x  %s\n", hf.Sum32(), file)
	}
	return fmt.Sprintf("%d", h.Sum32()), nil
}
