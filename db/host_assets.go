package db

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// AssetHostService is an app, service or endpoint projected onto one host row.
type AssetHostService struct {
	AssetID int64  `json:"asset_id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	URL     string `json:"url,omitempty"`
	Method  string `json:"method,omitempty"`
}

// AssetHostRow is the six-column host projection used by the Assets page.
type AssetHostRow struct {
	Key          string             `json:"key"`
	CompanyID    int64              `json:"company_id,omitempty"`
	CompanyName  string             `json:"company_name,omitempty"`
	RootDomain   string             `json:"root_domain,omitempty"`
	Subdomains   []string           `json:"subdomains"`
	IP           string             `json:"ip"`
	Services     []AssetHostService `json:"services"`
	AssetIDs     []int64            `json:"asset_ids"`
	FindingCount int                `json:"finding_count"`
	Critical     int                `json:"critical"`
	High         int                `json:"high"`
	Medium       int                `json:"medium"`
	Low          int                `json:"low"`
}

type AssetHostFilter struct {
	CompanyID int64
	TaskID    int64
	DSL       string
	Limit     int
	Offset    int
}

type hostFindingHit struct {
	id       int64
	severity string
	assetIDs []int64
}

// QueryHosts builds host rows from the same assets used by the existing type
// tabs, then applies filters and pagination to hosts rather than flat assets.
// shortcut: assemble in memory like the finding asset tree; move this projection
// to SQL when full asset scans become measurable in production.
func (s *AssetStore) QueryHosts(f AssetHostFilter) ([]AssetHostRow, int, error) {
	rows, err := s.db.Query(assetSelectCols + " ORDER BY id")
	if err != nil {
		return nil, 0, err
	}
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, 0, err
	}
	companyNames := map[int64]string{}
	companyRows, err := s.db.Query(`SELECT id, name FROM companies`)
	if err != nil {
		return nil, 0, err
	}
	for companyRows.Next() {
		var id int64
		var name string
		if err := companyRows.Scan(&id, &name); err != nil {
			companyRows.Close()
			return nil, 0, err
		}
		companyNames[id] = name
	}
	if err := companyRows.Close(); err != nil {
		return nil, 0, err
	}

	findingRows, err := s.db.Query(`SELECT id, COALESCE(severity,''), COALESCE(asset_ids::text,'[]') FROM findings`)
	if err != nil {
		return nil, 0, err
	}
	var hits []hostFindingHit
	for findingRows.Next() {
		var hit hostFindingHit
		var raw string
		if err := findingRows.Scan(&hit.id, &hit.severity, &raw); err != nil {
			findingRows.Close()
			return nil, 0, err
		}
		_ = json.Unmarshal([]byte(raw), &hit.assetIDs)
		hits = append(hits, hit)
	}
	if err := findingRows.Close(); err != nil {
		return nil, 0, err
	}

	var dslMatches map[int64]bool
	if strings.TrimSpace(f.DSL) != "" {
		matched, err := s.QueryDSL(f.DSL, "", f.TaskID, 0, 0)
		if err != nil {
			return nil, 0, err
		}
		dslMatches = map[int64]bool{}
		for _, a := range matched {
			dslMatches[a.ID] = true
		}
	}

	hosts := buildAssetHostRows(assets, companyNames, hits)
	filtered := hosts[:0]
	for _, row := range hosts {
		companyOK, taskOK, dslOK := f.CompanyID == 0, f.TaskID == 0, dslMatches == nil
		for _, a := range assets {
			if !containsAssetID(row.AssetIDs, a.ID) {
				continue
			}
			if a.CompanyID != nil && *a.CompanyID == f.CompanyID {
				companyOK = true
			}
			for _, taskID := range a.TaskIDs {
				if taskID == f.TaskID {
					taskOK = true
				}
			}
			if dslMatches[a.ID] {
				dslOK = true
			}
		}
		if companyOK && taskOK && dslOK {
			filtered = append(filtered, row)
		}
	}
	total := len(filtered)
	if f.Offset < 0 {
		f.Offset = 0
	}
	if f.Offset >= total {
		return []AssetHostRow{}, total, nil
	}
	filtered = filtered[f.Offset:]
	if f.Limit > 0 && len(filtered) > f.Limit {
		filtered = filtered[:f.Limit]
	}
	return filtered, total, nil
}

func containsAssetID(ids []int64, id int64) bool {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	return i < len(ids) && ids[i] == id
}

type assetHostBuilder struct {
	row          AssetHostRow
	assetSet     map[int64]bool
	rootSet      map[string]bool
	subdomainSet map[string]bool
	companyVotes map[int64]int
}

func newAssetHostBuilder(ip string) *assetHostBuilder {
	return &assetHostBuilder{
		row:      AssetHostRow{Key: "host:" + ip, IP: ip, Subdomains: []string{}, Services: []AssetHostService{}, AssetIDs: []int64{}},
		assetSet: map[int64]bool{}, rootSet: map[string]bool{}, subdomainSet: map[string]bool{}, companyVotes: map[int64]int{},
	}
}

func (b *assetHostBuilder) addBase(a *Asset) {
	if a.ID > 0 && !b.assetSet[a.ID] {
		b.assetSet[a.ID] = true
		b.row.AssetIDs = append(b.row.AssetIDs, a.ID)
	}
	if a.CompanyID != nil {
		b.companyVotes[*a.CompanyID]++
	}
	if a.RootDomain != "" {
		b.rootSet[strings.ToLower(a.RootDomain)] = true
	}
}

func buildAssetHostRows(assets []*Asset, companyNames map[int64]string, hits []hostFindingHit) []AssetHostRow {
	hosts := map[string]*assetHostBuilder{}
	domainRecords := map[string][]string{}
	ensure := func(ip string) *assetHostBuilder {
		ip = strings.TrimSpace(ip)
		if net.ParseIP(ip) == nil {
			return nil
		}
		if hosts[ip] == nil {
			hosts[ip] = newAssetHostBuilder(ip)
		}
		return hosts[ip]
	}
	for _, a := range assets {
		if a.Type == "ip" {
			if b := ensure(a.IP); b != nil {
				b.addBase(a)
			}
		}
		if a.Type == "subdomain" {
			domain := strings.ToLower(strings.TrimSuffix(a.Domain, "."))
			domainRecords[domain] = append(domainRecords[domain], a.RecordValue...)
		}
	}
	for _, a := range assets {
		if a.Type != "ip" {
			continue
		}
		for _, domain := range a.BoundDomains {
			domain = strings.ToLower(strings.TrimSuffix(domain, "."))
			domainRecords[domain] = append(domainRecords[domain], a.IP)
		}
	}

	var resolveDomain func(string, map[string]bool) []string
	resolveDomain = func(domain string, seen map[string]bool) []string {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" || seen[domain] {
			return nil
		}
		if net.ParseIP(domain) != nil {
			return []string{domain}
		}
		seen[domain] = true
		var out []string
		for _, value := range domainRecords[domain] {
			value = strings.TrimSpace(value)
			if net.ParseIP(value) != nil {
				out = append(out, value)
			} else {
				out = append(out, resolveDomain(value, seen)...)
			}
		}
		return out
	}

	for _, a := range assets {
		switch a.Type {
		case "subdomain":
			for _, ip := range resolveDomain(a.Domain, map[string]bool{}) {
				b := ensure(ip)
				b.addBase(a)
				b.subdomainSet[strings.ToLower(a.Domain)] = true
			}
		case "service", "endpoint", "app":
			ips := []string{}
			if net.ParseIP(a.IP) != nil {
				ips = []string{a.IP}
			} else {
				host := a.Domain
				if host == "" && a.URL != "" {
					if u, err := url.Parse(a.URL); err == nil {
						host = u.Hostname()
					}
				}
				ips = resolveDomain(host, map[string]bool{})
			}
			for _, ip := range ips {
				b := ensure(ip)
				b.addBase(a)
				label := hostServiceLabel(a)
				b.row.Services = append(b.row.Services, AssetHostService{AssetID: a.ID, Type: a.Type, Label: label, URL: a.URL, Method: a.Method})
			}
		}
	}

	assetHost := map[int64]*assetHostBuilder{}
	for _, b := range hosts {
		sort.Slice(b.row.AssetIDs, func(i, j int) bool { return b.row.AssetIDs[i] < b.row.AssetIDs[j] })
		for _, id := range b.row.AssetIDs {
			assetHost[id] = b
		}
		roots := make([]string, 0, len(b.rootSet))
		subdomains := make([]string, 0, len(b.subdomainSet))
		for root := range b.rootSet {
			roots = append(roots, root)
		}
		for domain := range b.subdomainSet {
			subdomains = append(subdomains, domain)
		}
		sort.Strings(roots)
		sort.Strings(subdomains)
		if len(roots) > 0 {
			b.row.RootDomain = roots[0]
		}
		b.row.Subdomains = subdomains
		sort.Slice(b.row.Services, func(i, j int) bool {
			if b.row.Services[i].Type != b.row.Services[j].Type {
				return b.row.Services[i].Type < b.row.Services[j].Type
			}
			return b.row.Services[i].Label < b.row.Services[j].Label
		})
		var bestCompany, bestVotes int64
		for id, votes := range b.companyVotes {
			if votes > int(bestVotes) || (votes == int(bestVotes) && (bestCompany == 0 || id < bestCompany)) {
				bestCompany, bestVotes = id, int64(votes)
			}
		}
		if bestCompany > 0 {
			b.row.CompanyID = bestCompany
			b.row.CompanyName = companyNames[bestCompany]
		}
	}
	for _, hit := range hits {
		seen := map[*assetHostBuilder]bool{}
		for _, assetID := range hit.assetIDs {
			b := assetHost[assetID]
			if b == nil || seen[b] {
				continue
			}
			seen[b] = true
			b.row.FindingCount++
			switch hit.severity {
			case "critical":
				b.row.Critical++
			case "high":
				b.row.High++
			case "medium":
				b.row.Medium++
			case "low":
				b.row.Low++
			}
		}
	}
	out := make([]AssetHostRow, 0, len(hosts))
	for _, b := range hosts {
		out = append(out, b.row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FindingCount != out[j].FindingCount {
			return out[i].FindingCount > out[j].FindingCount
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func hostServiceLabel(a *Asset) string {
	if a.Type == "app" && a.AppName != "" {
		return a.AppName
	}
	if a.Type == "endpoint" {
		return strings.TrimSpace(a.Method + " " + a.URL)
	}
	if a.URL != "" {
		return a.URL
	}
	if a.ServiceName != "" || a.ServiceType != "" {
		name := strings.TrimSpace(a.ServiceName + " " + a.ServiceType)
		if a.Port != nil {
			return name + ":" + strconv.Itoa(*a.Port)
		}
		return name
	}
	return fmt.Sprintf("asset #%d", a.ID)
}
