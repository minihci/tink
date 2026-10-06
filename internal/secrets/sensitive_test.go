package secrets

import "testing"

func TestSensitiveKey(t *testing.T) {
	for _, k := range []string{
		"environment.POSTGRES_PASSWORD", "environment.DB_PASSWORD", "environment.db-password", "environment.dbpassword",
		"environment.API_TOKEN", "environment.GITHUB_TOKEN", "environment.SECRET_KEY_BASE", "environment.AWS_SECRET_ACCESS_KEY",
		"environment.SSH_KEY", "environment.RESTIC_PASSWORD", "environment.PASSPHRASE", "environment.MY_APIKEY",
		"environment.CLIENT_SECRET", "user.password", "environment.AUTH",
	} {
		if !SensitiveKey(k) {
			t.Errorf("%q should be treated as sensitive", k)
		}
	}
	for _, k := range []string{
		"limits.memory", "boot.autostart", "environment.TZ", "environment.DB_HOSTNAME", "environment.REDIS_HOSTNAME",
		"environment.IMMICH_MACHINE_LEARNING_URL", "oci.entrypoint", "security.secureboot", "environment.PASSENGER_ENV",
		"environment.MONKEY", "volatile.eth0.hwaddr", "environment.KEYBOARD_LAYOUT",
	} {
		if SensitiveKey(k) {
			t.Errorf("%q should not be treated as sensitive (it would just be hidden for no reason)", k)
		}
	}
}

func TestMaskedConfig(t *testing.T) {
	in := map[string]string{"environment.DB_PASSWORD": "hunter2hunter2", "limits.memory": "1GiB"}
	got := MaskedConfig(in)
	if got["environment.DB_PASSWORD"] != HiddenValue || got["limits.memory"] != "1GiB" {
		t.Errorf("MaskedConfig = %v", got)
	}
	if in["environment.DB_PASSWORD"] != "hunter2hunter2" {
		t.Error("MaskedConfig must not modify its input")
	}
}
