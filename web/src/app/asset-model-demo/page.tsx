/**
 * asset-model-demo/page.tsx：目标资产模型演示（纯 mock，无后端依赖）。
 * 模型：任务=企业；IP 为核心资产（父域名/子域名/端口/服务下挂）；内网资产凭据表。
 * 复用 assets 页视觉语言：muted 表头、severity Badge、font-mono IP、点击行展开明细。
 */
"use client";

import * as React from "react";

import {
  BuildingIcon,
  ChevronRightIcon,
  GlobeIcon,
  KeyRoundIcon,
  NetworkIcon,
  RotateCcwIcon,
  ServerIcon,
  ShieldCheckIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

// =====================================================================
// Mock 数据：任务=企业
// =====================================================================

interface PortService {
  port: number;
  service: string;
  banner?: string;
  web?: boolean;
}

interface Credential {
  id: number;
  cred_type: string;
  username: string;
  domain: string;
  relation: "discovered" | "verified";
  target: string;
  source: string;
  asset_ip: string;
}

interface IPAsset {
  ip: string;
  c_segment: string;
  intranet: boolean;
  root_domain?: string; // 父域名
  subdomains: string[];
  ports: PortService[];
}

interface Enterprise {
  id: number;
  name: string;
  task_status: "running" | "done" | "paused";
  ips: IPAsset[];
  credentials: Credential[];
}

const ENTERPRISES: Enterprise[] = [
  {
    id: 101,
    name: "云帆科技",
    task_status: "running",
    ips: [
      {
        ip: "203.0.113.10",
        c_segment: "203.0.113.0/24",
        intranet: false,
        root_domain: "yunfan.com",
        subdomains: ["www.yunfan.com", "api.yunfan.com", "mail.yunfan.com"],
        ports: [
          { port: 80, service: "http", banner: "nginx 1.24", web: true },
          { port: 443, service: "https", banner: "nginx 1.24", web: true },
          { port: 22, service: "ssh", banner: "OpenSSH 8.9" },
        ],
      },
      {
        ip: "203.0.113.11",
        c_segment: "203.0.113.0/24",
        intranet: false,
        root_domain: "yunfan.com",
        subdomains: ["oa.yunfan.com"],
        ports: [
          { port: 8443, service: "https", banner: "Tomcat 9.0", web: true },
          { port: 3306, service: "mysql", banner: "MySQL 8.0.33" },
        ],
      },
      {
        ip: "10.20.1.15",
        c_segment: "10.20.1.0/24",
        intranet: true,
        subdomains: [],
        ports: [
          { port: 445, service: "smb", banner: "Windows Server 2019" },
          { port: 3389, service: "rdp" },
          { port: 135, service: "msrpc" },
        ],
      },
      {
        ip: "10.20.1.20",
        c_segment: "10.20.1.0/24",
        intranet: true,
        subdomains: [],
        ports: [
          { port: 22, service: "ssh", banner: "OpenSSH 7.4" },
          { port: 8080, service: "http", banner: "Jenkins 2.401", web: true },
        ],
      },
    ],
    credentials: [
      {
        id: 1,
        cred_type: "password",
        username: "Administrator",
        domain: "YUNFAN",
        relation: "verified",
        target: "smb://10.20.1.15",
        source: "lsass 转储",
        asset_ip: "10.20.1.15",
      },
      {
        id: 2,
        cred_type: "ntlm_hash",
        username: "svc_backup",
        domain: "YUNFAN",
        relation: "verified",
        target: "smb://10.20.1.15",
        source: "SAM 数据库",
        asset_ip: "10.20.1.15",
      },
      {
        id: 3,
        cred_type: "ssh_key",
        username: "jenkins",
        domain: "",
        relation: "verified",
        target: "ssh://10.20.1.20:22",
        source: "~/.ssh/id_rsa",
        asset_ip: "10.20.1.20",
      },
      {
        id: 4,
        cred_type: "password",
        username: "root",
        domain: "",
        relation: "discovered",
        target: "mysql://203.0.113.11:3306",
        source: "oa.yunfan.com 配置文件",
        asset_ip: "203.0.113.11",
      },
    ],
  },
  {
    id: 102,
    name: "星汉制造",
    task_status: "paused",
    ips: [
      {
        ip: "198.51.100.7",
        c_segment: "198.51.100.0/24",
        intranet: false,
        root_domain: "xinghan.cn",
        subdomains: ["www.xinghan.cn", "vpn.xinghan.cn"],
        ports: [
          { port: 443, service: "https", banner: "Apache 2.4", web: true },
          { port: 1194, service: "openvpn" },
        ],
      },
      {
        ip: "192.168.8.10",
        c_segment: "192.168.8.0/24",
        intranet: true,
        subdomains: [],
        ports: [
          { port: 445, service: "smb", banner: "Windows 10 22H2" },
          { port: 5985, service: "winrm" },
        ],
      },
    ],
    credentials: [
      {
        id: 5,
        cred_type: "password",
        username: "xinghan_admin",
        domain: "XINGHAN",
        relation: "discovered",
        target: "winrm://192.168.8.10:5985",
        source: "浏览器密码库",
        asset_ip: "192.168.8.10",
      },
    ],
  },
];

const TASK_STATUS: Record<Enterprise["task_status"], { label: string; className: string }> = {
  running: { label: "进行中", className: "bg-green-600 text-white" },
  done: { label: "已完成", className: "bg-slate-500 text-white" },
  paused: { label: "已暂停", className: "bg-yellow-500 text-black" },
};

function isVerified(c: Credential) {
  return c.relation === "verified";
}

// =====================================================================
// 页面
// =====================================================================

export default function AssetModelDemoPage() {
  const [enterpriseID, setEnterpriseID] = React.useState(ENTERPRISES[0].id);
  const [selectedIP, setSelectedIP] = React.useState<string | null>(null);

  const enterprise = ENTERPRISES.find((e) => e.id === enterpriseID) ?? ENTERPRISES[0];
  const selected = enterprise.ips.find((a) => a.ip === selectedIP) ?? null;
  const intranetCreds = enterprise.credentials.filter((c) =>
    enterprise.ips.some((a) => a.ip === c.asset_ip && a.intranet),
  );

  return (
    <div className="mx-auto max-w-[1200px] space-y-4 p-6">
      <header>
        <h1 className="flex items-center gap-2 text-lg font-semibold">
          <NetworkIcon className="size-5" />
          资产模型 Demo
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          任务 = 企业 · IP 为核心资产（父域名 / 子域名 / 端口服务下挂）· 内网资产凭据表
        </p>
      </header>

      {/* 企业 = 任务 */}
      <Tabs
        value={String(enterpriseID)}
        onValueChange={(v) => {
          setEnterpriseID(Number(v));
          setSelectedIP(null);
        }}
      >
        <TabsList>
          {ENTERPRISES.map((e) => (
            <TabsTrigger key={e.id} value={String(e.id)} className="gap-2">
              <BuildingIcon className="size-4" />
              {e.name}
              <span className="text-xs text-muted-foreground">任务 #{e.id}</span>
              <Badge className={TASK_STATUS[e.task_status].className}>{TASK_STATUS[e.task_status].label}</Badge>
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {/* IP 核心资产表 */}
      <div className="overflow-x-auto rounded-md border bg-card">
        <table className="w-full min-w-[1080px] text-sm">
          <thead className="bg-muted/40">
            <tr className="border-b text-left">
              <th className="w-8 px-2 py-3" />
              <th className="px-4 py-3 font-medium">IP（核心资产）</th>
              <th className="px-4 py-3 font-medium">C 段</th>
              <th className="px-4 py-3 font-medium">父域名</th>
              <th className="px-4 py-3 font-medium">子域名</th>
              <th className="px-4 py-3 font-medium">端口 / 服务</th>
              <th className="px-4 py-3 text-right font-medium">凭据</th>
            </tr>
          </thead>
          <tbody>
            {enterprise.ips.map((asset) => {
              const creds = enterprise.credentials.filter((c) => c.asset_ip === asset.ip);
              const verified = creds.filter(isVerified).length;
              const open = selectedIP === asset.ip;
              return (
                <tr
                  key={asset.ip}
                  tabIndex={0}
                  data-open={open || undefined}
                  className={cn(
                    "cursor-pointer border-b outline-none transition-colors last:border-0 focus-visible:ring-2 focus-visible:ring-ring",
                    open ? "bg-muted" : "hover:bg-muted/40",
                  )}
                  onClick={() => setSelectedIP(open ? null : asset.ip)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      setSelectedIP(open ? null : asset.ip);
                    }
                  }}
                >
                  <td className="px-2 py-3 text-muted-foreground">
                    <ChevronRightIcon className={cn("size-4 transition-transform", open && "rotate-90")} />
                  </td>
                  <td className="px-4 py-3">
                    <span className="font-mono text-xs font-medium">{asset.ip}</span>
                    {asset.intranet && (
                      <Badge variant="outline" className="ml-2 border-blue-500 text-blue-600">
                        内网
                      </Badge>
                    )}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{asset.c_segment}</td>
                  <td className="px-4 py-3">{asset.root_domain || "—"}</td>
                  <td className="max-w-[260px] px-4 py-3">
                    {asset.subdomains.length === 0 ? (
                      <span className="text-muted-foreground">—</span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {asset.subdomains.slice(0, 2).map((d) => (
                          <Badge key={d} variant="secondary" className="font-mono text-xs">
                            {d}
                          </Badge>
                        ))}
                        {asset.subdomains.length > 2 && (
                          <span className="text-xs text-muted-foreground" title={asset.subdomains.slice(2).join("\n")}>
                            +{asset.subdomains.length - 2}
                          </span>
                        )}
                      </div>
                    )}
                  </td>
                  <td className="max-w-[300px] px-4 py-3">
                    <div className="flex flex-wrap gap-1">
                      {asset.ports.map((p) => (
                        <Badge
                          key={p.port}
                          variant="outline"
                          className={cn("font-mono text-xs", p.web && "border-green-600 text-green-700")}
                        >
                          {p.port}/{p.service}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-3 text-right">
                    {creds.length === 0 ? (
                      <span className="text-muted-foreground">—</span>
                    ) : (
                      <span className="inline-flex items-center gap-1 text-xs">
                        <KeyRoundIcon className="size-3.5 text-muted-foreground" />
                        发现 {creds.length}
                        {verified > 0 && <span className="text-green-600"> / 已认证 {verified}</span>}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {/* IP 明细 */}
      {selected && (
        <section className="rounded-md border bg-card">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
            <div>
              <h2 className="flex items-center gap-2 text-sm font-medium">
                <ServerIcon className="size-4" />
                <span className="font-mono">{selected.ip}</span>
                {selected.intranet && (
                  <Badge variant="outline" className="border-blue-500 text-blue-600">
                    内网资产
                  </Badge>
                )}
              </h2>
              <p className="mt-1 text-xs text-muted-foreground">
                {selected.root_domain ? `父域名 ${selected.root_domain}` : "裸 IP"} · {selected.c_segment} ·{" "}
                {selected.ports.length} 个端口
              </p>
            </div>
            <Button variant="ghost" size="sm" onClick={() => setSelectedIP(null)}>
              <RotateCcwIcon className="size-4" />
              取消筛选
            </Button>
          </div>

          <div className="grid gap-0 md:grid-cols-2 md:divide-x">
            {/* 域名 */}
            <div className="px-4 py-3">
              <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <GlobeIcon className="size-3.5" />
                域名
              </h3>
              <dl className="mt-2 space-y-2 text-sm">
                <div className="flex gap-2">
                  <dt className="w-16 shrink-0 text-muted-foreground">父域名</dt>
                  <dd className="font-mono text-xs leading-5">{selected.root_domain || "—"}</dd>
                </div>
                <div className="flex gap-2">
                  <dt className="w-16 shrink-0 text-muted-foreground">子域名</dt>
                  <dd className="flex flex-wrap gap-1">
                    {selected.subdomains.length === 0 ? (
                      <span className="text-muted-foreground">—</span>
                    ) : (
                      selected.subdomains.map((d) => (
                        <Badge key={d} variant="secondary" className="font-mono text-xs">
                          {d}
                        </Badge>
                      ))
                    )}
                  </dd>
                </div>
              </dl>

              <h3 className="mt-4 flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <NetworkIcon className="size-3.5" />
                端口 / 服务
              </h3>
              <div className="mt-2 divide-y rounded-md border text-sm">
                {selected.ports.map((p) => (
                  <div key={p.port} className="flex items-center gap-3 px-3 py-2">
                    <span className="w-14 font-mono text-xs font-medium">{p.port}</span>
                    <Badge variant={p.web ? "default" : "outline"} className={cn(p.web && "bg-green-600 text-white")}>
                      {p.service}
                    </Badge>
                    <span className="truncate text-xs text-muted-foreground">{p.banner || ""}</span>
                  </div>
                ))}
              </div>
            </div>

            {/* 该 IP 凭据 */}
            <div className="px-4 py-3">
              <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <KeyRoundIcon className="size-3.5" />
                关联凭据
              </h3>
              <CredentialList
                items={enterprise.credentials.filter((c) => c.asset_ip === selected.ip)}
                empty="该资产暂无凭据。"
              />
            </div>
          </div>
        </section>
      )}

      {/* 内网资产凭据表 */}
      <section className="rounded-md border bg-card">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <h2 className="flex items-center gap-2 text-sm font-medium">
            <ShieldCheckIcon className="size-4" />
            内网资产凭据
            <Badge variant="secondary">{intranetCreds.length}</Badge>
          </h2>
          <span className="text-xs text-muted-foreground">
            仅统计内网（RFC1918）资产上的 discovered / verified 关系
          </span>
        </div>
        {intranetCreds.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground">该企业暂无内网凭据。</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[880px] text-sm">
              <thead className="bg-muted/40">
                <tr className="border-b text-left">
                  <th className="px-4 py-3 font-medium">状态</th>
                  <th className="px-4 py-3 font-medium">类型</th>
                  <th className="px-4 py-3 font-medium">账号</th>
                  <th className="px-4 py-3 font-medium">域</th>
                  <th className="px-4 py-3 font-medium">目标</th>
                  <th className="px-4 py-3 font-medium">来源 / 证据</th>
                  <th className="px-4 py-3 font-medium">所在资产</th>
                </tr>
              </thead>
              <tbody>
                {intranetCreds.map((c) => (
                  <tr key={c.id} className="border-b last:border-0 hover:bg-muted/40">
                    <td className="px-4 py-3">
                      {isVerified(c) ? (
                        <Badge className="bg-green-600 text-white">已认证</Badge>
                      ) : (
                        <Badge variant="outline">已发现</Badge>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <Badge variant="secondary">{c.cred_type}</Badge>
                    </td>
                    <td className="px-4 py-3 font-mono text-xs font-medium">{c.username}</td>
                    <td className="px-4 py-3 font-mono text-xs">{c.domain || "—"}</td>
                    <td className="px-4 py-3 font-mono text-xs">{c.target}</td>
                    <td className="max-w-[220px] truncate px-4 py-3 text-xs text-muted-foreground" title={c.source}>
                      {c.source}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs">{c.asset_ip}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

function CredentialList({ items, empty }: { items: Credential[]; empty: string }) {
  if (items.length === 0) {
    return <div className="mt-2 rounded-md border border-dashed p-4 text-sm text-muted-foreground">{empty}</div>;
  }
  return (
    <div className="mt-2 divide-y rounded-md border">
      {items.map((c) => (
        <div key={c.id} className="px-3 py-2 text-sm">
          <div className="flex items-center gap-2">
            {isVerified(c) ? (
              <Badge className="bg-green-600 text-white">已认证</Badge>
            ) : (
              <Badge variant="outline">已发现</Badge>
            )}
            <Badge variant="secondary">{c.cred_type}</Badge>
            <span className="font-mono text-xs font-medium">
              {c.domain ? `${c.domain}\\` : ""}
              {c.username}
            </span>
          </div>
          <div className="mt-1 font-mono text-xs text-muted-foreground">{c.target}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">来源：{c.source}</div>
        </div>
      ))}
    </div>
  );
}
