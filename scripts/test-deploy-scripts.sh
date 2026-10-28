#!/usr/bin/env bash
# 部署脚本的离线回归测试。fake docker/curl/go/npm/psql 只验证参数、失败分支
# 和文件生成，不替代真实 Docker/PostgreSQL 部署验收。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NODE_BIN="$(command -v node)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/artex-script-tests.XXXXXX")"
mkdir -p "$TMP/bin"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

cat > "$TMP/bin/docker" <<'EOF'
#!/usr/bin/env bash
{ printf 'docker'; printf ' %q' "$@"; printf '\n'; } >> "$FAKE_LOG"
if [[ " $* " == *" up "* && "${FAKE_UP_FAIL:-0}" == 1 ]]; then exit 1; fi
if [[ "${FAKE_DOCKER_CONSOLE:-0}" == 1 && " $* " == *" exec "* ]]; then
  cat >/dev/null
  if [[ "${FAKE_DSN_MARKER:-0}" == 1 ]]; then exit 1; fi
fi
exit 0
EOF
cat > "$TMP/bin/curl" <<'EOF'
#!/usr/bin/env bash
{ printf 'curl'; printf ' %q' "$@"; printf '\n'; } >> "$FAKE_LOG"
[[ "${FAKE_HEALTH_FAIL:-0}" != 1 ]]
EOF
cat > "$TMP/bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$TMP/bin/psql" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null
exit "${FAKE_PSQL_STATUS:-0}"
EOF
cat > "$TMP/bin/go" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == version ]]; then echo 'go version go1.26.3 test'; exit 0; fi
out=artex
while [[ $# -gt 0 ]]; do
  if [[ "$1" == -o ]]; then out="$2"; shift 2; else shift; fi
done
printf '#!/usr/bin/env bash\nexit 0\n' > "$out"
chmod +x "$out"
EOF
cat > "$TMP/bin/npm" <<'EOF'
#!/usr/bin/env bash
if [[ " $* " == *" build:static "* ]]; then mkdir -p out; echo test > out/index.html; fi
exit 0
EOF
chmod +x "$TMP/bin/"*

export FAKE_LOG="$TMP/commands.log"
: > "$FAKE_LOG"
export PATH="$TMP/bin:/usr/bin:/bin:/c/Windows/System32:/c/Windows/System32/WindowsPowerShell/v1.0"

new_case() {
  local name="$1"
  mkdir -p "$TMP/$name"
  cp "$ROOT/deploy.sh" "$TMP/$name/"
  (cd "$TMP/$name" && bash ./deploy.sh)
}

out="$(new_case first-run)" || fail "首次部署"
[[ -f "$TMP/first-run/.env" ]] || fail ".env 未生成"
password="$(awk -F= '$1=="POSTGRES_PASSWORD"{print $2}' "$TMP/first-run/.env")"
[[ ${#password} -eq 64 ]] || fail "随机密码长度=${#password}"
[[ "$out" != *"$password"* ]] || fail "默认输出泄露密码"
[[ "$out" != *"ARTEX_JWT_SECRET"* ]] || fail "输出引用无效 JWT 环境变量"
! grep -q '^ARTEX_JWT_SECRET=' "$TMP/first-run/.env" || fail ".env 引用无效 JWT 环境变量"
grep -q -- '--build' "$FAKE_LOG" || fail "默认未构建"
grep -q 'http://127.0.0.1:8787/api/health' "$FAKE_LOG" || fail "缺少 API 健康检查"
grep -q 'http://127.0.0.1:8787/' "$FAKE_LOG" || fail "缺少首页健康检查"
pass "首次部署生成安全 .env，默认不打印凭据"

: > "$FAKE_LOG"
out="$(cd "$TMP/first-run" && bash ./deploy.sh --no-build)" || fail "no-build"
grep -q -- '--no-build' "$FAKE_LOG" || fail "缺少 Compose --no-build"
! grep -q -- 'up -d --build' "$FAKE_LOG" || fail "no-build 同时出现 --build"
pass "--no-build 严格复用镜像"

mkdir -p "$TMP/weak"
cp "$ROOT/deploy.sh" "$TMP/weak/"
printf 'POSTGRES_PASSWORD=artex\n' > "$TMP/weak/.env"
if out="$(cd "$TMP/weak" && bash ./deploy.sh 2>&1)"; then fail "弱密码被接受"; fi
[[ "$out" == *"弱默认值"* ]] || fail "弱密码错误提示不明确"
pass "已有弱默认密码被拒绝"

mkdir -p "$TMP/up-fail"
cp "$ROOT/deploy.sh" "$TMP/up-fail/"
printf 'POSTGRES_PASSWORD=safe_pass-123\n' > "$TMP/up-fail/.env"
if out="$(cd "$TMP/up-fail" && FAKE_UP_FAIL=1 bash ./deploy.sh 2>&1)"; then fail "Compose up 失败被忽略"; fi
[[ "$out" == *"最近容器日志"* && "$out" == *"部署失败"* ]] || fail "up 失败缺诊断"
pass "Compose up 失败返回非零并输出诊断"

if out="$(cd "$TMP/up-fail" && FAKE_HEALTH_FAIL=1 bash ./deploy.sh 2>&1)"; then fail "健康失败被忽略"; fi
[[ "$out" == *"健康检查失败"* ]] || fail "健康失败提示缺失"
pass "健康检查失败返回非零"

mkdir -p "$TMP/update"
cp "$ROOT/update.sh" "$ROOT/deploy.sh" "$TMP/update/"
printf 'POSTGRES_PASSWORD=safe_pass-123\nARTEX_TAG=local\n' > "$TMP/update/.env"
: > "$FAKE_LOG"
out="$(cd "$TMP/update" && printf '1\n\n' | bash ./update.sh)" || fail "Docker 更新"
! grep -q 'pull artex' "$FAKE_LOG" || fail "仍尝试 pull 本地镜像"
grep -q -- '--build' "$FAKE_LOG" || fail "更新未重建源码"
pass "Docker 更新从当前源码重建"

mkdir -p "$TMP/install-local/web" "$TMP/install-local/cmd/artex"
cp "$ROOT/install.sh" "$TMP/install-local/"
out="$(cd "$TMP/install-local" && printf '2\n1\n127.0.0.1\n5432\nartex\np"a\\ss\nartex\ndisable\n' | bash ./install.sh)" || fail "本地安装"
[[ -f "$TMP/install-local/server/webui/dist/index.html" ]] || fail "前端 dist 未生成"
actual_password="$("$NODE_BIN" -e 'process.stdout.write(require(process.argv[1]).database.password)' "$TMP/install-local/config.json")"
[[ "$actual_password" == 'p"a\ss' ]] || fail "config JSON 转义错误"
case "${OSTYPE:-}" in
  msys|cygwin) ;;
  *) [[ "$(stat -c '%a' "$TMP/install-local/config.json")" == 600 ]] || fail "config 权限不是 600" ;;
esac
pass "本地安装处理 JSON 特殊字符、权限及前端目录"

mkdir -p "$TMP/reset"
cp "$ROOT/reset-password.sh" "$TMP/reset/"
out="$(cd "$TMP/reset" && bash ./reset-password.sh --dsn 'postgres://u:verysecret@example/db' -p 'long-enough' -y 2>&1)" || fail "DSN 重置模拟"
[[ "$out" != *"verysecret"* ]] || fail "DSN 密码泄露"
if local_fail_out="$(cd "$TMP/reset" && FAKE_PSQL_STATUS=1 bash ./reset-password.sh --dsn 'postgres://u:verysecret@example/db' -p 'long-enough' -y 2>&1)"; then
  fail "本地 psql 更新失败被忽略"
fi
[[ "$local_fail_out" != *"verysecret"* ]] || fail "本地失败输出泄露 DSN 密码"
if (cd "$TMP/reset" && bash ./reset-password.sh -p short -y >/dev/null 2>&1); then fail "短密码被接受"; fi
if (cd "$TMP/reset" && bash ./reset-password.sh -p "$(printf 'x%.0s' {1..73})" -y >/dev/null 2>&1); then fail "73 字节密码被接受"; fi
printf 'POSTGRES_USER=artex\nPOSTGRES_DB=artex\nPOSTGRES_PASSWORD=safe_pass\nMALICIOUS=$(touch pwned)\n' > "$TMP/reset/.env"
docker_out="$(cd "$TMP/reset" && FAKE_DOCKER_CONSOLE=1 bash ./reset-password.sh -m docker -p 'long-enough' -y 2>&1)" || fail "Docker 重置模拟"
[[ "$docker_out" != *"safe_pass"* && "$docker_out" != *"long-enough"* && "$docker_out" != *"postgres://unused"* ]] || fail "Docker 输出泄露凭据"
if docker_fail_out="$(cd "$TMP/reset" && FAKE_DOCKER_CONSOLE=1 FAKE_DSN_MARKER=1 bash ./reset-password.sh -m docker -p 'long-enough' -y 2>&1)"; then
  fail "Docker 控制台密码不一致被忽略"
fi
[[ "$docker_fail_out" != *"safe_pass"* && "$docker_fail_out" != *"long-enough"* && "$docker_fail_out" != *"postgres://unused"* ]] || fail "Docker 失败输出泄露凭据"
[[ ! -e "$TMP/reset/pwned" ]] || fail ".env 被当作 Shell 执行"
pass "密码重置长度、DSN 脱敏和 .env 安全解析"

mkdir -p "$TMP/import/export"
cp "$ROOT/start.sh" "$TMP/import/"
echo '{"name":"broken"}' > "$TMP/import/export/llm_profiles.jsonl"
if out="$(cd "$TMP/import" && ARTEX_DB_URL=postgres://unused FAKE_PSQL_STATUS=1 ./start.sh import-config export 2>&1)"; then fail "导入失败被报告为成功"; fi
[[ "$out" == *"导入 llm_profiles.jsonl 失败"* ]] || fail "导入失败提示缺失"
[[ "$out" != *"全部配置导入完成"* ]] || fail "失败后仍显示导入完成"
pass "配置导入失败正确中止"

echo "ALL SCRIPT TESTS PASSED"
