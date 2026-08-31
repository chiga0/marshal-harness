package productionruntime

import "testing"

func TestPi0844RequirementsClosedMapping(t *testing.T) {
	write, err := Pi0844Requirements("workspace-write")
	if err != nil || write.AccessMode != "workspace-write" || write.MinimumAssuranceLevel != "workspace-write" || write.Validate() != nil {
		t.Fatalf("workspace-write=%+v err=%v", write, err)
	}
	readOnly, err := Pi0844Requirements("read-only")
	if err != nil || readOnly.AccessMode != "read-only" || readOnly.Validate() != nil {
		t.Fatalf("read-only=%+v err=%v", readOnly, err)
	}
	if _, err := Pi0844Requirements("hardened"); err == nil {
		t.Fatal("hardened profile admitted by the ordinary-user dogfood mapping")
	}
	if _, err := Pi0844Requirements(""); err == nil {
		t.Fatal("empty profile admitted")
	}
}
