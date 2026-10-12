package resolve

import "testing"

func TestIncusSetEnv(t *testing.T) {
	noHome := ociRuntime{Env: []string{"PATH=/usr/bin", "NODE_VERSION=24"}}
	imageHome := ociRuntime{Env: []string{"HOME=/data/home"}}
	cases := []struct {
		name string
		k, v string
		rt   ociRuntime
		want bool
	}{
		{"TERM=xterm is Incus's", "environment.TERM", "xterm", noHome, true},
		{"another TERM is a person's", "environment.TERM", "screen", noHome, false},
		{"root's home when the image sets none", "environment.HOME", "/root", noHome, true},
		{"a non-root user's home when the image sets none", "environment.HOME", "/home/node", noHome, true},
		{"a HOME somewhere else is a person's choice", "environment.HOME", "/srv/app", noHome, false},
		{"a HOME the image defines is the image's, not Incus's (imageDerivedKeys decides)", "environment.HOME", "/root", imageHome, false},
		{"a home-looking value that only starts like one", "environment.HOME", "/homestead", noHome, false},
		{"anything else is not Incus's", "environment.TZ", "UTC", noHome, false},
	}
	for _, c := range cases {
		if got := incusSetEnv(c.k, c.v, c.rt); got != c.want {
			t.Errorf("%s: incusSetEnv(%q, %q) = %v, want %v", c.name, c.k, c.v, got, c.want)
		}
	}
}
