#!/bin/bash
# GitCode provider 端到端验证：一次调用内完成，避免 daemon 被会话清理连带杀死
AO="/c/Users/2026/AppData/Local/Programs/agent-orchestrator/resources/daemon/ao.exe"
LOG="C:/Users/2026/.ao/daemon-e2e.log"
export AO_GITCODE_TOKEN="${AO_GITCODE_TOKEN:?set AO_GITCODE_TOKEN (GitCode personal access token) before running}"

rm -f C:/Users/2026/.ao/running.json
"$AO" daemon > "$LOG" 2>&1 &
DPID=$!

for i in $(seq 1 45); do
  curl -s --connect-timeout 2 http://127.0.0.1:3001/readyz >/dev/null 2>&1 && break
  sleep 2
done
echo "== daemon ready (~$((i*2))s)"
"$AO" status | head -4

echo "== 1. project add"
"$AO" project add --path "I:/1/l/model-agent-pr" --name "model-agent-pr" --worker-agent claude-code
echo "== 2. set canonical upstream + worker agent"
"$AO" project set-config model-agent-pr --canonical-repo-url "https://gitcode.com/Ascend/model-agent.git" --worker-agent claude-code

echo "== 3. spawn session"
SPAWN_OUT=$("$AO" spawn --project model-agent-pr --harness claude-code --name "gc-e2e" --prompt "回复 ok 两个字即可，不要做任何修改。" 2>&1)
echo "$SPAWN_OUT"
SID=$(echo "$SPAWN_OUT" | grep -oE 'model-agent-pr-[0-9]+' | head -1)
echo "== session id: $SID"

echo "== 4. claim-pr #4645 (GitCode)"
"$AO" session claim-pr "$SID" 4645 2>&1 || "$AO" session claim-pr "$SID" "https://gitcode.com/Ascend/model-agent/merge_requests/4645" 2>&1

sleep 10
echo "== 5. session ls (看板列)"
"$AO" session ls
echo "== 6. session get (PR 详情)"
"$AO" session get "$SID"
echo "== 7. daemon 日志中的 gitcode 线索"
grep -i "gitcode" "$LOG" | head -15
grep -iE "scm observer|scm provider" "$LOG" | head -8

echo "== 8. cleanup: kill session"
"$AO" session kill "$SID" 2>&1
echo "== 9. stop daemon"
"$AO" stop 2>&1
echo "== DONE"
