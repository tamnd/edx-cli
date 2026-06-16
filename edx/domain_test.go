package edx_test

import (
	"testing"

	"github.com/tamnd/edx-cli/edx"
)

func TestDomainInfo(t *testing.T) {
	info := edx.Domain{}.Info()
	if info.Scheme != "edx" {
		t.Errorf("Scheme = %q, want edx", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != edx.Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, edx.Host)
	}
	if info.Identity.Binary != "edx" {
		t.Errorf("Identity.Binary = %q, want edx", info.Identity.Binary)
	}
}

func TestClassifyCourseSlug(t *testing.T) {
	cases := []struct {
		input   string
		wantTyp string
		wantID  string
		wantErr bool
	}{
		{"machine-learning/stanford-ml", "course", "machine-learning/stanford-ml", false},
		{"https://www.edx.org/learn/machine-learning/stanford-ml", "course", "machine-learning/stanford-ml", false},
		{"learn/machine-learning/stanford-ml", "course", "machine-learning/stanford-ml", false},
		{"stanford-ml", "course", "stanford-ml", false},
		{"", "", "", true},
	}
	for _, tc := range cases {
		typ, id, err := edx.Domain{}.Classify(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Classify(%q) expected error, got nil (typ=%q id=%q)", tc.input, typ, id)
			}
			continue
		}
		if err != nil {
			t.Errorf("Classify(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if typ != tc.wantTyp || id != tc.wantID {
			t.Errorf("Classify(%q) = (%q, %q), want (%q, %q)",
				tc.input, typ, id, tc.wantTyp, tc.wantID)
		}
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		uriType string
		id      string
		want    string
		wantErr bool
	}{
		{"course", "machine-learning/stanford-ml", "https://www.edx.org/learn/machine-learning/stanford-ml", false},
		{"course", "stanford-ml", "https://www.edx.org/learn/stanford-ml", false},
		{"unknown", "1", "", true},
	}
	for _, tc := range cases {
		got, err := edx.Domain{}.Locate(tc.uriType, tc.id)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Locate(%q, %q) expected error, got %q", tc.uriType, tc.id, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Locate(%q, %q) unexpected error: %v", tc.uriType, tc.id, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Locate(%q, %q) = %q, want %q", tc.uriType, tc.id, got, tc.want)
		}
	}
}
