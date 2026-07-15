package transport

import (
	"errors"
	"testing"
)

func TestIsMissingFTP(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "ftp 550 no such file",
			err:  errors.New(`550 "No such file or directory."`),
			want: true,
		},
		{
			name: "ftp unavailable",
			err:  errors.New("550 File unavailable"),
			want: true,
		},
		{
			name: "generic not found",
			err:  errors.New("resource not found"),
			want: true,
		},
		{
			name: "permission denied",
			err:  errors.New("530 Permission denied"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingFTP(tc.err); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}
