// stampede.js
// Simulates a high-concurrency cache stampede on a single cold key.
//
// WHAT THIS SCRIPT TESTS:
// A cache stampede occurs when high concurrent traffic requests a single item
// that is absent from Redis (a cold key or just-expired key).
//
// CONCURRENCY MECHANICS:
//   - Uses executor: 'per-vu-iterations' where all VUs (e.g. 200 or 600) fire
//     their request simultaneously in a single burst.
//   - Without coalescing, all requests attempt to acquire a database connection,
//     saturating the pool (20 connections) and triggering 503 timeouts.
//   - With local single-flight, requests on each node coalesce to 1 query per node.
//   - With distributed lock, requests across the entire cluster coalesce to strictly 1 query.

import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

// k6 configuration options
export const options = {
    summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(50)', 'p(75)', 'p(90)', 'p(95)', 'p(99)'],
    scenarios: {
        stampede: {
            executor: 'per-vu-iterations',
            vus: parseInt(__ENV.VUS || '200', 10),
            iterations: 1,
            maxDuration: '15s',
        },
    },
};

// Custom metrics
const hitRate = new Rate('cache_hit_rate');
const errorRate = new Rate('error_rate');
const errors503 = new Rate('http_503_errors');

export default function () {
    const targetUrl = __ENV.TARGET_URL || `${__ENV.BASE_URL || 'http://localhost:8080'}/api/items/42`;
    const strategy = __ENV.STRATEGY || 'no-cache';
    const delay = __ENV.DELAY || '0';
    const ttl = __ENV.TTL || '30';
    const jitter = __ENV.JITTER || '0.10';

    const url = `${targetUrl}?strategy=${strategy}&delay=${delay}&ttl=${ttl}&jitter=${jitter}`;

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

// Formats execution summary
export function handleSummary(data) {
    const totalReqs = data.metrics.http_reqs ? data.metrics.http_reqs.values.count : 0;
    const hitRateValue = data.metrics.cache_hit_rate ? data.metrics.cache_hit_rate.values.rate : 0;
    const hitCount = Math.round(totalReqs * hitRateValue);
    const missCount = totalReqs - hitCount - (data.metrics.error_rate ? data.metrics.error_rate.values.count : 0);

    console.log('\n======================================================');
    console.log('CacheCraft Stampede Execution Summary');
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
    console.log(`Total Errors: ${data.metrics.error_rate ? data.metrics.error_rate.values.count : 0}`);
    console.log(`HTTP 503 Errors (Pool Exhausted/Lock Timeout): ${data.metrics.http_503_errors ? data.metrics.http_503_errors.values.count : 0}`);
    console.log('======================================================\n');
    return {};
}
