package git

import (
	"testing"
)

func TestParseDiffNumstat(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFiles int
		wantAdd   int
		wantDel   int
	}{
		{
			name:      "empty output",
			input:     "",
			wantFiles: 0,
			wantAdd:   0,
			wantDel:   0,
		},
		{
			name:      "single file",
			input:     "10\t5\tsrc/main.go",
			wantFiles: 1,
			wantAdd:   10,
			wantDel:   5,
		},
		{
			name:      "multiple files",
			input:     "10\t5\tsrc/main.go\n20\t3\tsrc/util.go\n1\t0\tREADME.md",
			wantFiles: 3,
			wantAdd:   31,
			wantDel:   8,
		},
		{
			name:      "binary file",
			input:     "-\t-\tassets/logo.png",
			wantFiles: 1,
			wantAdd:   0,
			wantDel:   0,
		},
		{
			name:      "mixed binary and text",
			input:     "10\t5\tsrc/main.go\n-\t-\tassets/logo.png\n3\t1\tdocs/README.md",
			wantFiles: 3,
			wantAdd:   13,
			wantDel:   6,
		},
		{
			name:      "file with spaces in name",
			input:     "5\t2\tpath/to/my file.txt",
			wantFiles: 1,
			wantAdd:   5,
			wantDel:   2,
		},
		{
			name:      "trailing newline",
			input:     "10\t5\tsrc/main.go\n",
			wantFiles: 1,
			wantAdd:   10,
			wantDel:   5,
		},
		{
			name:      "only additions",
			input:     "100\t0\tnew_file.go",
			wantFiles: 1,
			wantAdd:   100,
			wantDel:   0,
		},
		{
			name:      "only deletions",
			input:     "0\t50\told_file.go",
			wantFiles: 1,
			wantAdd:   0,
			wantDel:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := parseDiffNumstat(tt.input)
			if stats.FilesChanged != tt.wantFiles {
				t.Errorf("FilesChanged = %d, want %d", stats.FilesChanged, tt.wantFiles)
			}
			if stats.Additions != tt.wantAdd {
				t.Errorf("Additions = %d, want %d", stats.Additions, tt.wantAdd)
			}
			if stats.Deletions != tt.wantDel {
				t.Errorf("Deletions = %d, want %d", stats.Deletions, tt.wantDel)
			}
		})
	}
}
