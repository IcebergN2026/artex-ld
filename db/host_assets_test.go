package db

import (
	"reflect"
	"strconv"
	"testing"
)

func TestBuildAssetHostRowsAggregatesHostAndDeduplicatesFindings(t *testing.T) {
	companyID := int64(1)
	port := 443
	assets := []*Asset{
		{ID: 1, Type: "ip", IP: "192.0.2.10", CompanyID: &companyID},
		{ID: 2, Type: "subdomain", Domain: "www.example.test", RootDomain: "example.test", RecordValue: []string{"192.0.2.10"}, CompanyID: &companyID},
		{ID: 3, Type: "subdomain", Domain: "api.example.test", RootDomain: "example.test", RecordValue: []string{"www.example.test"}},
		{ID: 4, Type: "service", Domain: "api.example.test", RootDomain: "example.test", URL: "https://api.example.test", Port: &port, CompanyID: &companyID},
		{ID: 5, Type: "endpoint", Domain: "api.example.test", RootDomain: "example.test", URL: "https://api.example.test/users", Method: "GET", CompanyID: &companyID},
		{ID: 6, Type: "ip", IP: "192.0.2.20"},
	}
	hits := []hostFindingHit{
		{id: 100, severity: "critical", assetIDs: []int64{4, 5}},
		{id: 101, severity: "low", assetIDs: []int64{5}},
	}
	rows := buildAssetHostRows(assets, map[int64]string{1: "示例企业"}, hits)
	if len(rows) != 2 {
		t.Fatalf("want two hosts, got %+v", rows)
	}
	host := rows[0]
	if host.IP != "192.0.2.10" || host.CompanyName != "示例企业" || host.RootDomain != "example.test" {
		t.Fatalf("wrong host projection: %+v", host)
	}
	if !reflect.DeepEqual(host.Subdomains, []string{"api.example.test", "www.example.test"}) {
		t.Fatalf("subdomains not aggregated: %+v", host.Subdomains)
	}
	if len(host.Services) != 2 || host.FindingCount != 2 || host.Critical != 1 || host.Low != 1 {
		t.Fatalf("services/findings not aggregated or deduplicated: %+v", host)
	}
	if !reflect.DeepEqual(host.AssetIDs, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("wrong row scope: %+v", host.AssetIDs)
	}
	bare := rows[1]
	if bare.IP != "192.0.2.20" || bare.RootDomain != "" || bare.Subdomains == nil || len(bare.Subdomains) != 0 || bare.FindingCount != 0 {
		t.Fatalf("bare IP must keep domains empty: %+v", bare)
	}
}

func TestQueryHostsAndExactAssetFindingFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	tk, err := d.CreateTask("宿主投影测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	taskIDs := "{" + strconv.FormatInt(tk.ID, 10) + "}"
	ipID := seedTreeAsset(t, d, "ip", map[string]any{"ip": "192.0.2.88", "task_ids": taskIDs})
	subID := seedTreeAsset(t, d, "subdomain", map[string]any{
		"domain": "api.host-projection.test", "root_domain": "host-projection.test",
		"record_value": []string{"192.0.2.88"}, "task_ids": taskIDs,
	})
	svcID := seedTreeAsset(t, d, "service", map[string]any{
		"domain": "api.host-projection.test", "root_domain": "host-projection.test",
		"url": "https://api.host-projection.test", "task_ids": taskIDs,
	})
	defer d.Assets().DeleteByIDs([]int64{ipID, subID, svcID}) //nolint:errcheck
	if _, err := d.AddFinding(tk.ID, 0, "Host", "host test", "high", "s", "e", "w", []int64{svcID}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := d.Assets().QueryHosts(AssetHostFilter{TaskID: tk.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].IP != "192.0.2.88" || rows[0].FindingCount != 1 {
		t.Fatalf("unexpected host query result: total=%d rows=%+v", total, rows)
	}
	items, findingTotal, err := d.ListFindingsPage(FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10), AssetIDs: rows[0].AssetIDs}, 1, 20)
	if err != nil || findingTotal != 1 || len(items) != 1 {
		t.Fatalf("exact host asset finding filter failed: total=%d items=%d err=%v", findingTotal, len(items), err)
	}
}
