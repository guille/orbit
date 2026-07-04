package main

import (
	"strings"
	"testing"
)

func TestDisableDismissPrompt(t *testing.T) {
	tests := []struct {
		name    string
		pending []string
		snoozed []string
		want    []string // substrings that must appear
	}{
		{
			name:    "single pending",
			pending: []string{"backup"},
			want:    []string{"'backup'", "pending", "dismiss"},
		},
		{
			name:    "single snoozed",
			snoozed: []string{"review"},
			want:    []string{"'review'", "snoozed", "cancel the snooze"},
		},
		{
			name:    "multiple pending only",
			pending: []string{"a", "b"},
			want:    []string{"dismiss 2 pending", "reminder(s)"},
		},
		{
			name:    "mixed pending and snoozed",
			pending: []string{"a", "b"},
			snoozed: []string{"c"},
			want:    []string{"dismiss 2 pending", "cancel snooze on 1", "and"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := disableDismissPrompt(tc.pending, tc.snoozed)
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("prompt %q missing %q", got, sub)
				}
			}
		})
	}
}
