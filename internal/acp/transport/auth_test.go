package transport

import "testing"

func TestBearerFromSubprotocol(t *testing.T) {
	for _, test := range []struct{ name, header, want string }{
		{"bearer", "acp, Bearer ytc_test", "ytc_test"},
		{"named", "yottacode.acp.v1, yottacode.bearer.ytc_test", "ytc_test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := bearerFromSubprotocol([]string{test.header}); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}
