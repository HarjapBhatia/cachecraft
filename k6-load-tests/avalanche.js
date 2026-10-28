// avalanche.js - Simulates synchronized key expiration cache avalanche dynamics.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

export const options = {
    summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(50)', 'p(75)', 'p(90)', 'p(95)', 'p(99)'],
    scenarios: {
        avalanche: {
            executor: 'constant-arrival-rate',
            rate: 2000, // 2000 requests per second
            timeUnit: '1s',
            duration: '10s',
            preAllocatedVUs: 100,
            maxVUs: 500,
        },
    },
};

const hitRate = new Rate('cache_hit_rate');
const errorRate = new Rate('error_rate');
const errors503 = new Rate('http_503_errors');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export function setup() {
    // 1. Reset Redis cache
    const resetRes = http.post(`${BASE_URL}/debug/cache/reset`);
    check(resetRes, { 'reset successful': (r) => r.status === 200 });

    // 2. Trigger bulk warm with absolute expiry
    const ttl = __ENV.TTL || 30;
    const warmRes = http.post(`${BASE_URL}/debug/cache/bulk-warm?ttl=${ttl}`);
    check(warmRes, { 'warm successful': (r) => r.status === 200 });

    const warmData = warmRes.json();
    const expiresAtMs = warmData.expiresAtEpochMs;

    console.log(`Cache warmed with ${warmData.warmedKeyCount} keys. Expiry epoch: ${expiresAtMs}`);

    // Wait until 1 second BEFORE expiry to start ramping up traffic
    const now = Date.now();
    let waitMs = expiresAtMs - now - 1000;
    if (waitMs < 0) {
        waitMs = 0;
    }

    console.log(`Waiting ${waitMs}ms before starting test load...`);
    sleep(waitMs / 1000);

    return { expiresAtMs };
}

export default function (data) {
    // Randomly select one of the 10,000 seeded items
    const id = Math.floor(Math.random() * 10000) + 1;
    const url = `${BASE_URL}/api/items/${id}?strategy=${__ENV.STRATEGY}&delay=${__ENV.DELAY}&ttl=${__ENV.TTL}&jitter=${__ENV.JITTER}`;

    const res = http.get(url);

    const success = check(res, {
        'status is 200': (r) => r.status === 200,
    });

    if (!success) {
        errorRate.add(1);
        if (res.status === 503) {
            errors503.add(1);
        }
    }

    const cacheHeader = res.headers['X-Cache'];
    if (cacheHeader === 'HIT') {
        hitRate.add(1);
    } else if (cacheHeader === 'MISS') {
        hitRate.add(0);
    }
}

export function handleSummary(data) {
    const totalReqs = data.metrics.http_reqs ? data.metrics.http_reqs.values.count : 0;
    const hitRateValue = data.metrics.cache_hit_rate ? data.metrics.cache_hit_rate.values.rate : 0;

    const hitCount = data.metrics.cache_hit_rate ? (data.metrics.cache_hit_rate.values.passes || 0) : 0;
    const missCount = data.metrics.cache_hit_rate ? (data.metrics.cache_hit_rate.values.fails || 0) : 0;
    const totalErrors = data.metrics.error_rate ? (data.metrics.error_rate.values.passes || 0) : 0;
    const errors503 = data.metrics.http_503_errors ? (data.metrics.http_503_errors.values.passes || 0) : 0;

    console.log('\n======================================================');
    console.log('CacheCraft Avalanche Execution Summary');
    console.log('======================================================');
    console.log(`Total Requests: ${totalReqs}`);
    console.log(`Throughput: ${data.metrics.http_reqs ? data.metrics.http_reqs.values.rate.toFixed(2) : '0'} req/s`);
    const dur = data.metrics.http_req_duration ? data.metrics.http_req_duration.values : {};
    const p50 = dur['p(50)'] !== undefined ? dur['p(50)'] : (dur.med || 0);
    const p75 = dur['p(75)'] !== undefined ? dur['p(75)'] : 0;
    const p90 = dur['p(90)'] !== undefined ? dur['p(90)'] : 0;
    const p95 = dur['p(95)'] !== undefined ? dur['p(95)'] : (dur.p95 || 0);
    const p99 = dur['p(99)'] !== undefined ? dur['p(99)'] : 0;
    const max = dur.max !== undefined ? dur.max : 0;
    console.log(`p50 Latency: ${p50.toFixed(2)} ms`);
    console.log(`p75 Latency: ${p75.toFixed(2)} ms`);
    console.log(`p90 Latency: ${p90.toFixed(2)} ms`);
    console.log(`p95 Latency: ${p95.toFixed(2)} ms`);
    console.log(`p99 Latency: ${p99.toFixed(2)} ms`);
    console.log(`Max Latency: ${max.toFixed(2)} ms`);
    console.log(`Cache Hit Rate: ${(hitRateValue * 100).toFixed(2)}%`);
    console.log(`Cache Hits: ${hitCount}`);
    console.log(`Cache Misses (X-Cache: MISS): ${missCount}`);
    console.log(`Total Errors: ${totalErrors}`);
    console.log(`HTTP 503 Errors (Pool Exhausted/Lock Timeout): ${errors503}`);
    console.log('======================================================\n');
    return {};
}
