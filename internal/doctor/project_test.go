package doctor

import (
	"errors"
	"testing"
)

func TestProjectChecks(t *testing.T) {
	r := &Report{}
	ProjectChecks("urbex.yaml", false, nil, r)
	if len(r.Checks) != 0 {
		t.Errorf("no urbex.yaml, no check: %+v", r.Checks)
	}
	ProjectChecks("urbex.yaml", true, nil, r)
	ProjectChecks("urbex.yaml", true, errors.New("urbex.yaml isn't in a git repository"), r)
	if len(r.Checks) != 2 || r.Checks[0].Status != OK || r.Checks[1].Status != Warn || r.Checks[1].Group != "project" || r.Checks[1].Fix == "" {
		t.Errorf("checks: %+v", r.Checks)
	}
	if r.Failed() {
		t.Error("an uncommitted urbex.yaml is a warning, not a failure")
	}
}
