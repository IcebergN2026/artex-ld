package agent

import (
	"strings"
	"testing"
)

func TestWorkerTemplatesIncludeDeliveryFactContract(t *testing.T) {
	for _, fragment := range []string{"[投放]", "record_fact", "目标 host/容器", "落地绝对路径", "每个投放物单独一条", "report_finding 不能代替投放登记", "失败或仅尝试上传不登记"} {
		if !strings.Contains(workerDefaultTmpl, fragment) {
			t.Errorf("default worker prompt missing %q", fragment)
		}
	}
	if !strings.Contains(workerDefaultTmpl, WorkerDeliveryFactSpec) {
		t.Fatal("default worker prompt must use the shared delivery contract")
	}
	for _, seed := range WorkerDomainSeeds() {
		if !strings.Contains(seed.Body, WorkerDeliveryFactSpec) {
			t.Errorf("scenario %s lost the delivery fact contract", seed.NKey)
		}
	}
}
