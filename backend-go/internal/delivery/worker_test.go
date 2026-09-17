package delivery

import (
	"errors"
	"testing"
)

// The two Redis replies the delivery consumer must tell apart: NOGROUP means
// the stream/group vanished and has to be recreated (2026-09-16 incident:
// Redis lost the key and the pool spun for 11h without recovering), while
// BUSYGROUP is the healthy "group already exists" answer on startup.
func TestStreamErrorClassification(t *testing.T) {
	noGroup := errors.New("NOGROUP No such key 'xiaoan3:queue:feishu_delivery' or consumer group 'deliverers' in XREADGROUP with GROUP option")
	busyGroup := errors.New("BUSYGROUP Consumer Group name already exists")

	if !isMissingGroup(noGroup) {
		t.Fatal("NOGROUP must be classified as a missing group (needs recreate)")
	}
	if isMissingGroup(busyGroup) {
		t.Fatal("BUSYGROUP must not be treated as a missing group")
	}
	if isMissingGroup(nil) {
		t.Fatal("nil must not be treated as a missing group")
	}
	if !isBusyGroup(busyGroup) {
		t.Fatal("BUSYGROUP must be classified as already-existing (expected, not a failure)")
	}
	if isBusyGroup(noGroup) {
		t.Fatal("NOGROUP must not be treated as BUSYGROUP")
	}
	if isBusyGroup(nil) {
		t.Fatal("nil must not be treated as BUSYGROUP")
	}
}
