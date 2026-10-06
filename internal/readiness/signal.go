// Package readiness validates disposable scheduling hints, never execution authority.
package readiness

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"google.golang.org/protobuf/proto"
	"strings"
)

const TTLMS int64 = 30_000

func Fresh(signal *pb.ExecutionSignal, received, now int64) *pb.ExecutionSignal {
	if signal == nil || received <= 0 || received > now || now-received > TTLMS || signal.ObservedAtMs <= 0 || signal.ObservedAtMs > now+5000 || now-signal.ObservedAtMs > TTLMS || signal.MeasuredAtMs <= 0 || signal.MeasuredAtMs > signal.ObservedAtMs || now-signal.MeasuredAtMs > TTLMS || signal.WorkspacePrepareP50Ms < 0 || signal.WorkspacePrepareP50Ms > 3600000 || signal.EnvironmentStartupP50Ms < 0 || signal.EnvironmentStartupP50Ms > 3600000 || signal.FreeSlots < 0 || signal.FreeSlots > 1024 {
		return nil
	}
	for _, list := range [][]string{signal.RuntimeReady, signal.EnvironmentReady, signal.TemplateFingerprints, signal.RepositoryRefs} {
		if len(list) > 32 {
			return nil
		}
		for _, v := range list {
			if strings.TrimSpace(v) == "" || len(v) > 512 {
				return nil
			}
		}
	}
	return proto.Clone(signal).(*pb.ExecutionSignal)
}
