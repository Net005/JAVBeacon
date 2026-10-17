package download

import "testing"

func TestPathMappingMatchesDirectoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path, prefix string
		want         bool
	}{
		{"/downloads/example.mp4", "/downloads", true},
		{"/downloads", "/downloads", true},
		{"/downloads/example.mp4", "/downloads/", true},
		{"/downloads-other/example.mp4", "/downloads", false},
		{"/downloads-other/example.mp4", "/downloads/", false},
		{"/downloads/example.mp4", "", false},
		{"/downloads/example.mp4", "/", true},
		{`C:\downloads\example.mp4`, `C:\downloads`, true},
		{`C:\downloads-other\example.mp4`, `C:\downloads`, false},
	} {
		t.Run(tc.path+"/"+tc.prefix, func(t *testing.T) {
			if got := matchesPathPrefix(tc.path, tc.prefix); got != tc.want {
				t.Fatalf("path mapping match = %v, want %v", got, tc.want)
			}
		})
	}
}
