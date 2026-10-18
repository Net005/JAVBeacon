package domain

import "testing"

func TestBlacklistURLIdentity(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://downloads.example.com/Case.mp4?file=1#preview", "https://downloads.example.com/Case.mp4?file=1"},
		{"https://mypikpak.com/s/SyntheticShare/OtherFile?act=play", "https://mypikpak.com/s/SyntheticShare"},
		{"https://mypikpak.com/s/SyntheticShare?act=preview", "https://mypikpak.com/s/SyntheticShare"},
		{"magnet:?xt=urn:btih:ABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD&dn=title", "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd"},
		{"magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "magnet:?xt=urn:btih:0000000000000000000000000000000000000000"},
	}
	for _, tc := range cases {
		got, err := BlacklistURLKey(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("%q: %q %v, expected %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"", "javascript:alert(1)", "file:///movie.mp4", "magnet:?xt=invalid", "https://user:pass@downloads.example.com/movie.mp4"} {
		if _, err := BlacklistURLKey(raw); err == nil {
			t.Fatalf("invalid source accepted: %q", raw)
		}
	}
}
