package resolve

import "strings"

// incusSetEnv reports an environment.* key (with its value v) that Incus wrote when it created an OCI instance, as opposed to the image or a
// person: TERM=xterm, and HOME as the home directory of the user the process runs as, when the image does not define HOME itself. That is
// /root for root, and /home/<name> for an image that names another user (the Matter server image runs as uid 1000 and gets /home/node).
//
// A HOME the image defines is the image's own, and is handled by imageDerivedKeys; a HOME that is neither under /home nor /root is something a
// person chose and is kept.
func incusSetEnv(k, v string, rt ociRuntime) bool {
	switch k {
	case "environment.TERM":
		return v == "xterm"
	case "environment.HOME":
		for _, kv := range rt.Env {
			if strings.HasPrefix(kv, "HOME=") {
				return false
			}
		}
		return v == "/root" || strings.HasPrefix(v, "/home/")
	}
	return false
}
