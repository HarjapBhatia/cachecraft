#!/bin/bash
# run-test.sh
# Automated benchmark harness for CacheCraft load experiments.
#
# ENVIRONMENT CONFIGURATION:
# Automatically loads defaults from .env if present.
# You do NOT need to pass target=... on every run.
#
# USAGE EXAMPLES (Clean & Simple):
#   bash ./run-test.sh type=baseline strat=cache-aside delay=0 ttl=300
#   bash ./run-test.sh type=stampede strat=no-cache n=200 delay=250 node=1
#   bash ./run-test.sh type=stampede strat=cache-aside n=600 delay=250
#   bash ./run-test.sh type=stampede strat=local-single-flight n=600 delay=250
#   bash ./run-test.sh type=stampede strat=distributed-lock n=600 delay=250
#   bash ./run-test.sh type=avalanche strat=cache-aside ttl=5 delay=5 jitter=0.0
#   bash ./run-test.sh type=avalanche strat=cache-aside-jitter ttl=5 delay=5 jitter=0.10

set -e

# Load defaults from .env file if it exists
if [ -f .env ]; then
  # Export non-comment lines
  export $(grep -v '^#' .env | xargs)
fi

# Default parameter values
TYPE="baseline"
STRAT="no-cache"
N="600"
TTL="30"
DELAY="0"
JITTER="0.10"
NODE=""

# Use BASE_URL from .env or default to http://localhost:8080
BASE_URL="${BASE_URL:-http://localhost:8080}"
TARGET_URL="${TARGET_URL:-${BASE_URL}/api/items/42}"

# Parse key=value arguments from the command line
for arg in "$@"; do
  key=$(echo "$arg" | cut -f1 -d=)
  val=$(echo "$arg" | cut -f2 -d=)
  case $key in
    type) TYPE=$val ;;
    strat) STRAT=$val ;;
    n) N=$val ;;
    ttl) TTL=$val ;;
    delay) DELAY=$val ;;
    jitter) JITTER=$val ;;
    node) NODE=$val ;;
    target) TARGET_URL=$val ;;
    *) echo "Unknown argument: $key"; exit 1 ;;
  esac
done

# If user specified node=1, node=2, or node=3, target that specific node directly
if [ -n "$NODE" ]; then
  case $NODE in
    1) TARGET_URL="http://localhost:8081/api/items/42" ;;
    2) TARGET_URL="http://localhost:8082/api/items/42" ;;
    3) TARGET_URL="http://localhost:8083/api/items/42" ;;
    *) echo "Unknown node: $NODE (valid: 1, 2, 3)"; exit 1 ;;
  esac
fi

SCRIPT="k6-load-tests/${TYPE}.js"
if [ ! -f "$SCRIPT" ]; then
    echo "Error: Test script '$SCRIPT' not found."
    exit 1
fi

echo "======================================================"
echo "Running $TYPE test"
echo "  Strategy:   $STRAT"
echo "  VUs:        $N"
echo "  Target:     $TARGET_URL"
echo "  Delay:      ${DELAY}ms"
echo "  TTL:        ${TTL}s"
echo "  Jitter:     $JITTER"
echo "======================================================"

# Pre-flight check: ensure the cluster is running and reachable
if ! curl -s -f -o /dev/null --connect-timeout 2 "$BASE_URL/debug/db-queries" 2>/dev/null; then
    echo ""
    echo "ERROR: Cannot connect to CacheCraft cluster at $BASE_URL (Connection Refused)."
    echo "The containers are not running or still starting up."
    echo ""
    echo "To start the cluster, run:"
    echo "  docker compose up --build -d"
    echo ""
    echo "To check container status, run:"
    echo "  docker compose ps"
    echo ""
    exit 1
fi

# Fetches the dbQueryCount integer from a specific API instance by port
get_query_count() {
    local port=$1
    local res
    res=$(curl -s "http://localhost:${port}/debug/db-queries" || echo '{"dbQueryCount":0}')
    echo "$res" | grep -o '"dbQueryCount":[0-9]*' | cut -d':' -f2 || echo 0
}

# 1. Take snapshot of SQL statement counters before running the test
before_1=$(get_query_count 8081)
before_2=$(get_query_count 8082)
before_3=$(get_query_count 8083)

echo "Before DB Queries - API 1: $before_1, API 2: $before_2, API 3: $before_3"

# 2. Export environment variables and execute k6 load test
export BASE_URL="$BASE_URL"
export TARGET_URL="$TARGET_URL"
export STRATEGY="$STRAT"
export DELAY="$DELAY"
export TTL="$TTL"
export JITTER="$JITTER"
export VUS="$N"

k6 run "$SCRIPT"

# 3. Take snapshot of SQL statement counters after test completion
after_1=$(get_query_count 8081)
after_2=$(get_query_count 8082)
after_3=$(get_query_count 8083)

echo "After DB Queries  - API 1: $after_1, API 2: $after_2, API 3: $after_3"

# 4. Calculate query deltas for each node and total delta
delta_1=$((after_1 - before_1))
delta_2=$((after_2 - before_2))
delta_3=$((after_3 - before_3))
total_delta=$((delta_1 + delta_2 + delta_3))

echo "======================================================"
echo "Repository Query Evidence (Total Delta): $total_delta"
echo " (API 1: +$delta_1 | API 2: +$delta_2 | API 3: +$delta_3)"
echo "======================================================"

# 5. Distributed lock assertion: strictly 1 query allowed across all 3 nodes
if [[ "$STRAT" == "distributed-lock" && "$TYPE" == "stampede" && $total_delta -gt 1 ]]; then
    echo "WARNING: Distributed lock test expected exactly 1 query, but saw $total_delta."
fi
