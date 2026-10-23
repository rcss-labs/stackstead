package api

import "testing"

func TestExecTimeoutCoupling(t *testing.T) {
	if execFlushWait < 2*execWriteTimeout {
		t.Errorf("execFlushWait %v must be at least twice execWriteTimeout %v", execFlushWait, execWriteTimeout)
	}
	if execWriteTimeout > execPongTimeout-execPing {
		t.Errorf("execWriteTimeout %v exceeds PongTimeout-Ping %v", execWriteTimeout, execPongTimeout-execPing)
	}
}
