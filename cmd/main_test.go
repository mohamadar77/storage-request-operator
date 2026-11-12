/*
Copyright 2026.

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

package main

import (
	"slices"
	"testing"
)

const (
	nsA = "team-a"
	nsB = "team-b"
)

func TestWatchNamespaceConfig(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // nil means cluster-scoped (nil map)
	}{
		{name: "empty is cluster-scoped", in: "", want: nil},
		{name: "whitespace only is cluster-scoped", in: "  ,  , ", want: nil},
		{name: "single namespace", in: nsA, want: []string{nsA}},
		{name: "multiple namespaces", in: nsA + "," + nsB, want: []string{nsA, nsB}},
		{name: "trims spaces and drops blanks", in: " " + nsA + " , , " + nsB + " ", want: []string{nsA, nsB}},
		{name: "deduplicates", in: nsA + "," + nsA, want: []string{nsA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := watchNamespaceConfig(tt.in)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("watchNamespaceConfig(%q) = %v, want nil (cluster-scoped)", tt.in, got)
				}
				return
			}
			keys := make([]string, 0, len(got))
			for k := range got {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tt.want) {
				t.Errorf("watchNamespaceConfig(%q) keys = %v, want %v", tt.in, keys, tt.want)
			}
		})
	}
}
