package uml

import "testing"

const mountinfo = `25 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
30 25 0:25 / /dev/shm rw,nosuid,nodev,noexec - tmpfs shm rw,size=65536k
31 25 0:26 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
32 25 0:27 / /run/user/1000 rw,nosuid,nodev,relatime - tmpfs tmpfs rw
33 25 0:28 / /var/tmp rw,relatime - overlay overlay rw
`

func TestExecTmpfs(t *testing.T) {
	for dir, want := range map[string]bool{"/dev/shm": false, "/tmp": true, "/tmp/sub": true, "/run/user/1000": true, "/var/tmp": false, "/home": false} {
		if got, mp := execTmpfs(mountinfo, dir); got != want {
			t.Errorf("%s: got %v (mount %s), want %v", dir, got, mp, want)
		}
	}
}

func TestArgs(t *testing.T) {
	a := Args("/r", "/kvmx-init", 256, "foo=bar")
	want := map[string]bool{"mem=256M": false, "rootfstype=hostfs": false, "rootflags=/r": false, "init=/kvmx-init": false, "foo=bar": false}
	for _, x := range a {
		if _, ok := want[x]; ok {
			want[x] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("missing %s in %v", k, a)
		}
	}
}
