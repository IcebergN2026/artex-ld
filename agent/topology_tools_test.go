package agent

import (
	"strings"
	"testing"
)

func TestInternalWorkerTopologyRelationContract(t *testing.T) {
	for _, seed := range WorkerDomainSeeds() {
		if seed.NKey != "内网渗透" {
			continue
		}
		for _, want := range []string{"record_topology_relation", "evidence_ref=fact:", "禁止因同任务、同网段或存在入口而猜测连线"} {
			if !strings.Contains(seed.Body, want) {
				t.Fatalf("internal Worker prompt missing %q", want)
			}
		}
		return
	}
	t.Fatal("internal Worker template not found")
}
