// Zweep media type for Zabbix 7.0+ (webhook script).
// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only
//
// Parameters: server_url (one Zweep server; for more servers clone the media type), zweep_source,
// secret, allow_plaintext (lab only), plus the event macros.
// Any failure throws: Zabbix marks the alert failed, retries it, and the escalation continues.
// The script never reports success unless the server answered 2xx (i.e. committed the event).
try {
    var p = JSON.parse(value);
    var source = String(p.zweep_source || '');
    var secret = String(p.secret || '');
    var url = String(p.server_url || '').replace(/^\s+|\s+$/g, '').replace(/\/+$/, '');
    if (url === '') {
        throw 'server_url missing';
    }
    if (/[,;\s]/.test(url)) {
        throw 'server_url must be a single URL: clone the media type for each Zweep server';
    }
    if (!/^https:\/\//.test(url) && p.allow_plaintext !== 'true') {
        throw 'server_url must use https (allow_plaintext=true only in lab mode): ' + url;
    }
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(source)) {
        throw 'zweep_source missing or invalid';
    }
    if (secret.length < 32) {
        throw 'secret missing or too short';
    }
    delete p.server_url;
    delete p.secret;
    delete p.allow_plaintext;

    var body = JSON.stringify(p);
    var ts = String(Math.floor(Date.now() / 1000));
    var req = new HttpRequest();
    req.addHeader('Content-Type: application/json');
    req.addHeader('X-Zweep-Source: ' + source);
    req.addHeader('X-Zweep-Timestamp: ' + ts);
    req.addHeader('X-Zweep-Signature: v1=' + hmac('sha256', secret, ts + '.' + body));
    var resp = req.post(url + '/v1/zabbix/webhook', body);
    var status = req.getStatus();
    if (status >= 200 && status < 300) {
        return 'OK';
    }
    throw 'HTTP ' + status + ' ' + String(resp).substring(0, 200);
} catch (error) {
    Zabbix.log(3, '[Zweep] delivery failed: ' + error);
    throw 'Zweep delivery failed: ' + error;
}
