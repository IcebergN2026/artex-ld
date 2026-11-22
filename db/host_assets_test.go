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

func TestBuildAssetHostRowsPortsAndIntranet(t *testing.T) {
	assets := []*Asset{
		{ID: 1, Type: "ip", IP: "10.1.2.3", OpenPorts: []map[string]any{
			{"port": float64(443), "service": "https"},
			{"port": float64(22), "service": "ssh"},
			{"port": float64(443), "service": "https"},
		}},
		{ID: 2, Type: "ip", IP: "192.0.2.9"},
	}
	rows := buildAssetHostRows(assets, nil, nil)
	byIP := map[string]AssetHostRow{}
	for _, row := range rows {
		byIP[row.IP] = row
	}
	private, ok := byIP["10.1.2.3"]
	if !ok {
		t.Fatalf("private host missing: %+v", rows)
	}
	if !private.Intranet {
		t.Fatalf("10.1.2.3 must be intranet: %+v", private)
	}
	if !reflect.DeepEqual(private.Ports, []PortService{{Port: 22, Service: "ssh"}, {Port: 443, Service: "https"}}) {
		t.Fatalf("ports not deduped/sorted: %+v", private.Ports)
	}
	public, ok := byIP["192.0.2.9"]
	if !ok {
		t.Fatalf("public host missing: %+v", rows)
	}
	if public.Intranet {
		t.Fatalf("192.0.2.9 must not be intranet: %+v", public)
	}
	if len(public.Ports) != 0 {
		t.Fatalf("ports must stay empty: %+v", public.Ports)
	}
}

func TestQueryHostsIntranetFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	tk, err := d.CreateTask("内外网过滤测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	taskIDs := "{" + strconv.FormatInt(tk.ID, 10) + "}"
	intID := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.66.0.9", "task_ids": taskIDs})
	extID := seedTreeAsset(t, d, "ip", map[string]any{"ip": "192.0.2.99", "task_ids": taskIDs})
	defer d.Assets().DeleteByIDs([]int64{intID, extID}) //nolint:errcheck

	tr, fa := true, false
	cases := []struct {
		name     string
		intranet *bool
		wantIP   string
	}{
		{"内网", &tr, "10.66.0.9"},
		{"外网", &fa, "192.0.2.99"},
	}
	for _, tc := range cases {
		rows, total, err := d.Assets().QueryHosts(AssetHostFilter{TaskID: tk.ID, Intranet: tc.intranet, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(rows) != 1 || rows[0].IP != tc.wantIP {
			t.Fatalf("%s 过滤错误: total=%d rows=%+v", tc.name, total, rows)
		}
	}
	rows, total, err := d.Assets().QueryHosts(AssetHostFilter{TaskID: tk.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("nil 不过滤应返回全部: total=%d rows=%+v", total, rows)
	}
}

func TestBuildAssetHostRowsServiceFingerprint(t *testing.T) {
	status := 200
	port := 443
	assets := []*Asset{
		{ID: 1, Type: "ip", IP: "10.9.8.7", OpenPorts: []map[string]any{
			{"port": float64(22), "service": "ssh", "product": "OpenSSH", "version": "8.9p1"},
			{"port": float64(443), "service": "https", "product": "nginx", "version": "1.24.0"},
		}},
		{ID: 2, Type: "service", IP: "10.9.8.7", URL: "https://app.internal", Port: &port,
			Technologies: []string{"Vue 3", "nginx"}, PageTitle: "控制台", StatusCode: &status},
	}
	rows := buildAssetHostRows(assets, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("want one host, got %+v", rows)
	}
	host := rows[0]
	wantPorts := []PortService{
		{Port: 22, Service: "ssh", Product: "OpenSSH", Version: "8.9p1"},
		{Port: 443, Service: "https", Product: "nginx", Version: "1.24.0"},
	}
	if !reflect.DeepEqual(host.Ports, wantPorts) {
		t.Fatalf("fingerprint ports: %+v", host.Ports)
	}
	if len(host.Services) != 1 {
		t.Fatalf("want one service, got %+v", host.Services)
	}
	svc := host.Services[0]
	if svc.Port == nil || *svc.Port != 443 || svc.PageTitle != "控制台" || svc.StatusCode == nil || *svc.StatusCode != 200 {
		t.Fatalf("service fingerprint fields: %+v", svc)
	}
	if !reflect.DeepEqual(svc.Technologies, []string{"Vue 3", "nginx"}) {
		t.Fatalf("technologies: %+v", svc.Technologies)
	}
}
