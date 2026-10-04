// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.net

import net.nicodroid.zweep.core.WireJson
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ApiJsonTest {
    /** Real response of /v1/app/config (e2e 2026-09-29): Go encodes an empty slice as null */
    @Test
    fun configWithNullsDecodes() {
        val json = """{"channels":[{"id":"sev_5","kind":"severity","name":"Disaster","enabled":true}],"device_id":"d","features":["receipts"],
            "hostgroups":null,"keepalive_s":60,"node_id":"e2e","server_id":"726be637","service_urls":null,
            "sources":[{"id":"zbx-01","name":"ZBX lab","api_mode":"disabled"}],"track_shown":true,"username":"mario"}"""
        val cfg = WireJson.decodeFromString(ConfigResponse.serializer(), json)
        assertTrue(cfg.serviceUrls.isEmpty())
        assertTrue(cfg.hostgroups.isEmpty())
        assertEquals("sev_5", cfg.channels.single().id)
        assertEquals("disabled", cfg.sources.single().apiMode)
    }

    @Test
    fun snapshotAndDetailDecode() {
        val snap = WireJson.decodeFromString(Snapshot.serializer(), """{"rev":3,"stale":false,"sources":[{"id":"zbx","stale":true}],"checksum":"sha256:x",
            "perimeter":true,"problems":[{"source":"zbx","eventid":"9","status":"open","name":"n","severity":4,"clock":"2026-09-29T10:00:00Z",
            "hosts":[{"hostid":"1","host":"h","name":"H"}],"hostgroups":["G"],"tags":null,"version":2}]}""")
        assertEquals(1, snap.problems.size)
        assertTrue(snap.problems[0].tags.isEmpty())
        val det = WireJson.decodeFromString(Detail.serializer(), """{"problem":{"source":"zbx","eventid":"9","status":"acknowledged","name":"n",
            "severity":4,"clock":"2026-09-29T10:00:00Z","version":2},"history":[{"clock":"2026-09-29T10:01:00Z","author_kind":"app_user",
            "author_name":"mario","via_service_user":true,"actions":["ack","message"],"message":"on it"}],"frontend_url":"https://z/tr_events.php"}""")
        assertEquals("mario", det.history.single().authorName)
    }
}
