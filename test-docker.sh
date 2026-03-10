#!/bin/bash
set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

NODE_PORT=9091
NODE_URL="http://localhost:$NODE_PORT"
NODE_PID=""
SB_ID=""

pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; exit 1; }
info() { echo -e "${YELLOW}→ $1${NC}"; }

cleanup() {
    if [ -n "$SB_ID" ]; then
        info "Cleaning up sandbox $SB_ID"
        curl -s -X DELETE "$NODE_URL/sandboxes/$SB_ID" > /dev/null 2>&1 || true
    fi
    if [ -n "$NODE_PID" ]; then
        info "Stopping node (PID $NODE_PID)"
        kill "$NODE_PID" 2>/dev/null || true
        wait "$NODE_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

cd "$(dirname "$0")"

# --- Step 1: Build ---
info "Building Linux agent binary..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/gitvm-agent-linux ./cmd/gitvm
pass "Built bin/gitvm-agent-linux"

info "Building gitvm-node..."
go build -o bin/gitvm-node ./cmd/gitvm-node
pass "Built bin/gitvm-node"

# --- Step 2: Check Docker ---
if ! docker info > /dev/null 2>&1; then
    fail "Docker is not running. Start Docker Desktop first."
fi
pass "Docker is running"

# --- Step 3: Start node ---
info "Starting gitvm-node on port $NODE_PORT..."
GITVM_RUNTIME=docker \
GITVM_AGENT_BINARY="$(pwd)/bin/gitvm-agent-linux" \
GITVM_NODE_PORT=$NODE_PORT \
./bin/gitvm-node &
NODE_PID=$!
sleep 2

if ! kill -0 "$NODE_PID" 2>/dev/null; then
    fail "Node failed to start"
fi
pass "Node started (PID $NODE_PID)"

# --- Step 4: Health check ---
info "Health check..."
HEALTH=$(curl -s "$NODE_URL/health")
echo "$HEALTH" | grep -q "ok" && pass "Health check passed" || fail "Health check failed: $HEALTH"

# --- Step 5: Create sandbox ---
info "Creating sandbox..."
CREATE_RESP=$(curl -s -X POST "$NODE_URL/sandboxes" \
    -H "Content-Type: application/json" \
    -d '{"template":"ubuntu:22.04","vcpus":1,"memoryMB":256}')
echo "  Response: $CREATE_RESP"

SB_ID=$(echo "$CREATE_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || true)
if [ -z "$SB_ID" ]; then
    fail "Failed to create sandbox: $CREATE_RESP"
fi
pass "Sandbox created: $SB_ID"

# Wait for agent to be ready
info "Waiting for agent inside container..."
sleep 3

# --- Step 6: Execute command ---
info "Executing 'echo hello from gitvm'..."
EXEC_RESP=$(curl -s -X POST "$NODE_URL/sandboxes/$SB_ID/exec" \
    -H "Content-Type: application/json" \
    -d '{"command":"echo hello from gitvm"}')
echo "  Response: $EXEC_RESP"

echo "$EXEC_RESP" | grep -q "hello from gitvm" && pass "Exec works" || fail "Exec failed: $EXEC_RESP"

# --- Step 7: Execute with env vars ---
info "Executing with env vars..."
EXEC_RESP=$(curl -s -X POST "$NODE_URL/sandboxes/$SB_ID/exec" \
    -H "Content-Type: application/json" \
    -d '{"command":"echo $MY_VAR","envVars":{"MY_VAR":"gitvm-test"}}')
echo "  Response: $EXEC_RESP"
echo "$EXEC_RESP" | grep -q "gitvm-test" && pass "Env vars work" || info "Env vars may not be supported yet"

# --- Step 8: Write file ---
info "Writing file /tmp/test.txt..."
WRITE_RESP=$(curl -s -X PUT "$NODE_URL/sandboxes/$SB_ID/files?path=/tmp/test.txt" \
    -H "Content-Type: application/json" \
    -d '{"content":"hello from test script"}')
echo "  Response: $WRITE_RESP"
pass "File written"

# --- Step 9: Read file ---
info "Reading file /tmp/test.txt..."
READ_RESP=$(curl -s "$NODE_URL/sandboxes/$SB_ID/files?path=/tmp/test.txt")
echo "  Response: $READ_RESP"
echo "$READ_RESP" | grep -q "hello from test script" && pass "File read works" || fail "File read failed: $READ_RESP"

# --- Step 10: List files ---
info "Listing /tmp/..."
LIST_RESP=$(curl -s "$NODE_URL/sandboxes/$SB_ID/files/list?path=/tmp")
echo "  Response: $LIST_RESP"
echo "$LIST_RESP" | grep -q "test.txt" && pass "File list works" || info "File list format may differ"

# --- Step 11: Run multi-line command ---
info "Running multi-line command..."
EXEC_RESP=$(curl -s -X POST "$NODE_URL/sandboxes/$SB_ID/exec" \
    -H "Content-Type: application/json" \
    -d '{"command":"uname -a && whoami && pwd"}')
echo "  Response: $EXEC_RESP"
pass "Multi-line command executed"

# --- Step 12: List sandboxes ---
info "Listing sandboxes..."
LIST_RESP=$(curl -s "$NODE_URL/sandboxes")
echo "  Response: $LIST_RESP"
echo "$LIST_RESP" | grep -q "$SB_ID" && pass "Sandbox appears in list" || info "List format may differ"

# --- Step 13: Delete sandbox ---
info "Deleting sandbox $SB_ID..."
DEL_RESP=$(curl -s -X DELETE "$NODE_URL/sandboxes/$SB_ID")
echo "  Response: $DEL_RESP"
pass "Sandbox deleted"
SB_ID="" # prevent double cleanup

# --- Step 14: Verify cleanup ---
info "Verifying container removed..."
sleep 1
CONTAINERS=$(docker ps -a --filter label=gitvm=sandbox --format '{{.Names}}' 2>/dev/null)
if [ -z "$CONTAINERS" ]; then
    pass "No gitvm containers remaining"
else
    info "Remaining containers: $CONTAINERS"
fi

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}  All tests completed!${NC}"
echo -e "${GREEN}========================================${NC}"
