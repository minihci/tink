package backupext

import (
	"strings"
	"testing"
)

type fake struct{ Backup }

func (fake) PolicyProto() int { return 7 }

func TestGetPanicsRatherThanPlanWithoutTheBackupRules(t *testing.T) {
	old := impl
	defer func() { impl = old }()
	impl = nil
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "no backup capability registered") {
			t.Errorf("recovered %v", r)
		}
	}()
	Get()
}

func TestRegisterReplacesTheCapability(t *testing.T) {
	old := impl
	defer func() { impl = old }()
	Register(fake{})
	if got := Get().PolicyProto(); got != 7 {
		t.Errorf("PolicyProto = %d, want the registered capability's", got)
	}
}
