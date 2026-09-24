package job

import "testing"

func TestValidateExternalExecutionRequest(t *testing.T) {
	r := ExternalExecutionRequest{ExecutionID:"exec-1", Workload:ExternalWorkload{Type:"agent"}, Input:ExternalInput{Goal:"fix bug"}}
	if err := ValidateExternalExecutionRequest(r); err != nil { t.Fatal(err) }
	r.ExecutionID = ""
	if err := ValidateExternalExecutionRequest(r); err == nil { t.Fatal("expected validation error") }
}
