package server

import "testing"

func TestResolveSpawnTaskName(t *testing.T) {
	tests := []struct {
		name      string
		scenario  string
		raw       string
		assetKeys []string
		want      string
	}{
		{name: "internal IP fills empty name", scenario: "内网渗透", assetKeys: []string{"ip:192.168.9.101"}, want: "内网渗透·192.168.9.101"},
		{name: "internal domain normalizes legacy name", scenario: "内网渗透", raw: "内网渗透 · old.example", assetKeys: []string{"domain:target.example"}, want: "内网渗透·target.example"},
		{name: "internal name cannot contain arbitrary evidence", scenario: "内网渗透", raw: "一段很长的证据摘要", assetKeys: []string{"ip:10.0.0.8"}, want: "内网渗透·10.0.0.8"},
		{name: "other scenario keeps provided name", scenario: "权限提升", raw: "  权限提升·host  ", want: "权限提升·host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSpawnTaskName(tt.scenario, tt.raw, tt.assetKeys); got != tt.want {
				t.Fatalf("resolveSpawnTaskName() = %q, want %q", got, tt.want)
			}
		})
	}
}
