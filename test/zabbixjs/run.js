// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only
//
// Runs a Zabbix webhook script with a minimal emulation of the Zabbix JavaScript environment:
// synchronous HttpRequest, hmac(), sha256(), Zabbix.log(). Test tool only.
// usage: node run.js <script.js> <params.json>   (prints {"ok":bool,"result":...} as JSON)
'use strict';
const fs = require('fs');
const crypto = require('crypto');
const { spawnSync } = require('child_process');

const script = fs.readFileSync(process.argv[2], 'utf8');
const params = fs.readFileSync(process.argv[3], 'utf8');

// One synchronous HTTP POST, performed by a child process
const httpHelper = `
const [url, headers, body] = JSON.parse(process.argv[1]);
const h = {};
for (const line of headers) { const i = line.indexOf(':'); h[line.slice(0, i).trim()] = line.slice(i + 1).trim(); }
fetch(url, { method: 'POST', headers: h, body, signal: AbortSignal.timeout(5000) })
  .then(async r => process.stdout.write(JSON.stringify({ status: r.status, body: await r.text() })))
  .catch(e => process.stdout.write(JSON.stringify({ error: String(e.cause ? e.cause.code || e.cause : e) })));
`;

function HttpRequest() {
    this.headers = [];
    this.status = 0;
}
HttpRequest.prototype.addHeader = function (h) { this.headers.push(h); };
HttpRequest.prototype.getStatus = function () { return this.status; };
HttpRequest.prototype.post = function (url, body) {
    const r = spawnSync(process.execPath, ['-e', httpHelper, JSON.stringify([url, this.headers, body])], { encoding: 'utf8' });
    const res = JSON.parse(r.stdout || '{"error":"no output"}');
    if (res.error) {
        throw 'cannot send request: ' + res.error;
    }
    this.status = res.status;
    return res.body;
};

const logs = [];
const Zabbix = { log: (level, msg) => logs.push(msg) };
const hmac = (alg, key, data) => crypto.createHmac(alg, key).update(data).digest('hex');
const sha256 = data => crypto.createHash('sha256').update(data).digest('hex');

const fn = new Function('value', 'HttpRequest', 'Zabbix', 'hmac', 'sha256', script);
let out;
try {
    out = { ok: true, result: fn(params, HttpRequest, Zabbix, hmac, sha256), logs };
} catch (e) {
    out = { ok: false, result: String(e), logs };
}
process.stdout.write(JSON.stringify(out));
