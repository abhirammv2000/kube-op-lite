package v1alpha1

import (
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	t.Run("fills in priority and replicas when unset", func(t *testing.T) {
		w := &GpuWorkload{}
		w.Default()
		if w.Spec.Priority != PriorityMedium {
			t.Errorf("priority = %q, want %q", w.Spec.Priority, PriorityMedium)
		}
		if w.Spec.Replicas != 1 {
			t.Errorf("replicas = %d, want 1", w.Spec.Replicas)
		}
	})

	t.Run("keeps values that are already set", func(t *testing.T) {
		w := &GpuWorkload{Spec: GpuWorkloadSpec{Priority: PriorityHigh, Replicas: 5}}
		w.Default()
		if w.Spec.Priority != PriorityHigh || w.Spec.Replicas != 5 {
			t.Errorf("defaults overwrote set values: %+v", w.Spec)
		}
	})
}

func TestValidate(t *testing.T) {
	valid := GpuWorkloadSpec{Replicas: 1, Image: "nginx:1.27", GpuCount: 2, Priority: PriorityMedium}

	tests := []struct {
		name        string
		mutate      func(*GpuWorkloadSpec)
		wantErrs    []string // substrings that must all appear in the error
		wantWarning bool
	}{
		{name: "a valid spec passes", mutate: func(*GpuWorkloadSpec) {}},
		{name: "empty priority is allowed (defaulted later)", mutate: func(s *GpuWorkloadSpec) { s.Priority = "" }},
		{name: "zero GPUs is allowed", mutate: func(s *GpuWorkloadSpec) { s.GpuCount = 0 }},
		{name: "eight GPUs is the maximum", mutate: func(s *GpuWorkloadSpec) { s.GpuCount = 8 }, wantWarning: true},
		{name: "zero replicas is rejected", mutate: func(s *GpuWorkloadSpec) { s.Replicas = 0 }, wantErrs: []string{"spec.replicas must be >= 1"}},
		{name: "negative GPUs is rejected", mutate: func(s *GpuWorkloadSpec) { s.GpuCount = -1 }, wantErrs: []string{"spec.gpuCount must be >= 0"}},
		{name: "more than eight GPUs is rejected", mutate: func(s *GpuWorkloadSpec) { s.GpuCount = 9 }, wantErrs: []string{"spec.gpuCount must be <= 8"}, wantWarning: true},
		{name: "empty image is rejected", mutate: func(s *GpuWorkloadSpec) { s.Image = "" }, wantErrs: []string{"spec.image must not be empty"}},
		{name: "unknown priority is rejected", mutate: func(s *GpuWorkloadSpec) { s.Priority = "urgent" }, wantErrs: []string{"spec.priority must be one of"}},
		{
			name:     "every problem is reported together",
			mutate:   func(s *GpuWorkloadSpec) { s.Replicas = 0; s.Image = ""; s.Priority = "x" },
			wantErrs: []string{"spec.replicas", "spec.image", "spec.priority"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			tt.mutate(&spec)
			w := &GpuWorkload{Spec: spec}

			warnings, err := w.ValidateCreate()
			if len(tt.wantErrs) == 0 && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tt.wantErrs) > 0 {
				if err == nil {
					t.Fatalf("expected an error containing %v, got none", tt.wantErrs)
				}
				for _, want := range tt.wantErrs {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err.Error(), want)
					}
				}
			}
			if tt.wantWarning != (len(warnings) > 0) {
				t.Errorf("warnings = %v, wantWarning = %v", warnings, tt.wantWarning)
			}

			// Updates are validated by the same rules.
			_, updateErr := w.ValidateUpdate(&GpuWorkload{})
			if (err == nil) != (updateErr == nil) {
				t.Errorf("ValidateCreate err = %v but ValidateUpdate err = %v", err, updateErr)
			}
		})
	}

	t.Run("deleting is always allowed", func(t *testing.T) {
		w := &GpuWorkload{}
		if _, err := w.ValidateDelete(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
