package history

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestParseVersionNum(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    int
	}{
		{"initial version", "initial", -1},
		{"v1", "v1", 1},
		{"v10", "v10", 10},
		{"v300", "v300", 300},
		{"v0", "v0", 0},
		{"unparseable", "xyz", -2},
		{"v with non-number", "vabc", -2},
		{"empty string", "", -2},
		{"timestamp fallback", "v1738000000", 1738000000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVersionNum(tt.version)
			if got != tt.want {
				t.Errorf("parseVersionNum(%q) = %d, want %d", tt.version, got, tt.want)
			}
		})
	}
}

func TestFindMaxVersion(t *testing.T) {
	tests := []struct {
		name    string
		files   []db.File
		wantVer string
	}{
		{
			name:    "single initial",
			files:   []db.File{{Version: "initial", ID: "a"}},
			wantVer: "initial",
		},
		{
			name: "initial and v1",
			files: []db.File{
				{Version: "initial", ID: "a"},
				{Version: "v1", ID: "b"},
			},
			wantVer: "v1",
		},
		{
			name: "out of order with same timestamp",
			files: []db.File{
				{Version: "v1", ID: "a", CreatedAt: 1000},
				{Version: "v5", ID: "b", CreatedAt: 1000},
				{Version: "v3", ID: "c", CreatedAt: 1000},
			},
			wantVer: "v5",
		},
		{
			name: "v2 before v10 lexicographic trap",
			files: []db.File{
				{Version: "v2", ID: "a"},
				{Version: "v10", ID: "b"},
			},
			wantVer: "v10",
		},
		{
			name: "mixed parseable and unparseable",
			files: []db.File{
				{Version: "xyz", ID: "a"},
				{Version: "v3", ID: "b"},
				{Version: "initial", ID: "c"},
			},
			wantVer: "v3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMaxVersion(tt.files)
			if got.Version != tt.wantVer {
				t.Errorf("findMaxVersion() version = %q, want %q", got.Version, tt.wantVer)
			}
		})
	}
}

func TestLatestByPath(t *testing.T) {
	tests := []struct {
		name    string
		files   []db.File
		wantMap map[string]string
	}{
		{
			name: "single file single path",
			files: []db.File{
				{Path: "/a.go", Version: "v1", ID: "1"},
			},
			wantMap: map[string]string{"/a.go": "v1"},
		},
		{
			name: "multiple versions same path same timestamp",
			files: []db.File{
				{Path: "/a.go", Version: "initial", ID: "1", CreatedAt: 1000},
				{Path: "/a.go", Version: "v1", ID: "2", CreatedAt: 1000},
				{Path: "/a.go", Version: "v2", ID: "3", CreatedAt: 1000},
				{Path: "/a.go", Version: "v3", ID: "4", CreatedAt: 1000},
			},
			wantMap: map[string]string{"/a.go": "v3"},
		},
		{
			name: "multiple paths",
			files: []db.File{
				{Path: "/a.go", Version: "v2", ID: "1", CreatedAt: 1000},
				{Path: "/a.go", Version: "v5", ID: "2", CreatedAt: 1000},
				{Path: "/b.go", Version: "initial", ID: "3", CreatedAt: 1000},
				{Path: "/b.go", Version: "v1", ID: "4", CreatedAt: 1000},
			},
			wantMap: map[string]string{
				"/a.go": "v5",
				"/b.go": "v1",
			},
		},
		{
			name: "multiple paths different timestamps but wrong order",
			files: []db.File{
				{Path: "/a.go", Version: "v1", ID: "1", CreatedAt: 2000},
				{Path: "/a.go", Version: "v3", ID: "2", CreatedAt: 1000},
				{Path: "/a.go", Version: "v2", ID: "3", CreatedAt: 1000},
			},
			wantMap: map[string]string{"/a.go": "v3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := latestByPath(tt.files)
			if len(got) != len(tt.wantMap) {
				t.Fatalf("latestByPath() returned %d files, want %d", len(got), len(tt.wantMap))
			}
			for _, f := range got {
				wantVer, ok := tt.wantMap[f.Path]
				if !ok {
					t.Errorf("unexpected path %q in result", f.Path)
					continue
				}
				if f.Version != wantVer {
					t.Errorf("path %q: version = %q, want %q", f.Path, f.Version, wantVer)
				}
			}
		})
	}
}
