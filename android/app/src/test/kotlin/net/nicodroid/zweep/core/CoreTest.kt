// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CoreTest {
    @Test
    fun seqTrackerContiguousHolesAndDuplicates() {
        val t = SeqTracker(0)
        assertEquals(SeqTracker.Result.New(-1), t.persisted(1))
        assertEquals(1, t.acked)
        // Hole: 2 missing, ask once from 2
        assertEquals(SeqTracker.Result.New(2), t.persisted(3))
        assertEquals(1, t.acked)
        assertEquals(SeqTracker.Result.New(-1), t.persisted(4)) // resync already requested
        assertEquals(SeqTracker.Result.New(-1), t.persisted(2))
        assertEquals(4, t.acked)
        assertEquals(SeqTracker.Result.Duplicate, t.persisted(3))
        // Gap beyond retention: the position jumps, never waits forever
        t.gap(10)
        assertEquals(10, t.acked)
        t.persisted(11)
        assertEquals(11, t.acked)
    }

    @Test
    fun seqTrackerFreshInstallAdoptsServerPosition() {
        val t = SeqTracker(-1)
        t.adopt(41)
        t.persisted(42)
        assertEquals(42, t.acked)
        t.adopt(5) // only once
        assertEquals(42, t.acked)
    }

    @Test
    fun parsesServerFrames() {
        val msg = parseFrame("""{"type":"msg","id":"0192","seq":7,"sid":"zbx:1","ver":10000001234,"kind":"problem","sev":5,
            "channels":["sev_5"],"source":"zbx","ts":1234,"title":"[Disaster] h: x","body":{"host":"h","event_id":1},"extra":1}""")
        assertTrue(msg is Incoming.Msg)
        assertEquals(7, (msg as Incoming.Msg).msg.seq)
        val w = parseFrame("""{"type":"welcome","start_seq":3,"head_seq":9,"oldest_seq":1,"proj_rev":4,"features":["problems"],"server_id":"abc"}""")
        assertEquals("abc", (w as Incoming.Hello).welcome.serverId)
        val d = parseFrame("""{"type":"problems.delta","from_rev":1,"to_rev":2,"upsert":[{"source":"zbx","eventid":"5","status":"open",
            "name":"x","severity":4,"clock":"2026-09-29T10:00:00Z","version":3}],"remove":[{"source":"zbx","eventid":"4"}]}""")
        assertEquals(1, (d as Incoming.Delta).delta.upsert.size)
        // ack.result: eventid as a number (older server) and as a string
        val r1 = parseFrame("""{"eventid":68,"request_id":"r1","source":"zbx","state":"confirmed","type":"ack.result"}""")
        assertEquals("confirmed", (r1 as Incoming.AckDone).result.state)
        val r2 = parseFrame("""{"eventid":"68","request_id":"r2","source":"zbx","state":"rejected","reason":"zabbix_unavailable","type":"ack.result"}""")
        assertEquals("zabbix_unavailable", (r2 as Incoming.AckDone).result.reason)
        assertTrue(parseFrame("not json") is Incoming.Unknown)
        assertTrue(parseFrame("""{"type":"msg","seq":"bad"}""") is Incoming.Unknown)
    }

    @Test
    fun rotationNeverDropsMoreSevereUnresolved() {
        val cap = Rotation.alarmCap(1) // the service notification
        assertEquals(19, cap)
        val full = (1..cap).map { Shown("k$it", 5, false, it.toLong()) }
        assertNull(Rotation.victim(full, 3, cap)) // all disasters open: a new average is only stored
        assertFalse(Rotation.hasRoom(full, 3, cap))
        val withResolved = full.toMutableList().also { it[10] = Shown("r", 5, true, 100) }
        assertEquals("r", Rotation.victim(withResolved, 1, cap)?.key)
        assertEquals("r", Rotation.victim(withResolved, -1, cap)?.key) // a recovery takes the place of a resolved alarm
        assertNull(Rotation.victim(full, -1, cap)) // but never of an open one
        val mixed = (1..cap).map { Shown("k$it", if (it < 4) 2 else 5, false, it.toLong()) }
        assertEquals("k1", Rotation.victim(mixed, 4, cap)?.key) // lowest severity, oldest
        assertEquals(emptyList<Shown>(), Rotation.victims(mixed.take(10), 0, cap)) // not full
    }

    @Test
    fun rotationStaysUnderTheSafeTotalOfEveryAndroid() {
        // Samsung refuses from the 26th notification of an app: the alarms plus the others never exceed the safe total
        assertTrue(Rotation.SAFE_TOTAL <= 20)
        // After an upgrade with too many alarms on screen, enough of them go to fit the new one
        val over = (1..24).map { Shown("k$it", 2, it % 2 == 0, it.toLong()) }
        val cap = Rotation.alarmCap(3) // service, a system notice, a group summary
        val v = Rotation.victims(over, 4, cap)!!
        assertEquals(over.size - cap + 1, v.size)
        assertTrue(v.take(12).all { it.resolved }) // resolved ones first
        // System notices take room from the alarms, never beyond the total
        assertEquals(1, Rotation.alarmCap(40))
    }

    @Test
    fun pacerAllowsFourPerSecond() {
        val p = Pacer(4)
        repeat(4) { i ->
            assertEquals(0, p.delayFor(1000L + i))
            p.posted(1000L + i)
        }
        assertEquals(1000, p.delayFor(1003) + 3)
        assertEquals(0, p.delayFor(2001))
    }

    @Test
    fun presentationRules() {
        val now = 10_000_000L
        assertEquals(Presentation.Decision.Silent(NotShownReason.APP_DND), Presentation.decide(now, now, now + 1, true, true))
        assertEquals(Presentation.Decision.Silent(NotShownReason.OLD), Presentation.decide(now, now - 3_600_001, 0, true, true))
        assertEquals(Presentation.Decision.Silent(NotShownReason.CHANNEL_OFF), Presentation.decide(now, now, 0, false, true))
        assertEquals(Presentation.Decision.Notify, Presentation.decide(now, now - 1000, 0, true, true))
    }

    @Test
    fun reminderDefaults() {
        assertTrue(ChannelPrefs.defaultsFor(Severity.DISASTER).remindAgain(1000))
        assertTrue(ChannelPrefs.defaultsFor(Severity.HIGH).remindAgain(2))
        assertFalse(ChannelPrefs.defaultsFor(Severity.HIGH).remindAgain(3))
        assertFalse(ChannelPrefs.defaultsFor(Severity.WARNING).remindAgain(0))
    }

    @Test
    fun checksumMatchesServerFormat() {
        // Same rows as the server test vector: "zbx:00000000000000000005:3\n"
        val rows = listOf(ProblemRow("zbx", "5", "open", "x", 4, "2026-09-29T10:00:00Z", version = 3))
        val expected = "sha256:" + java.security.MessageDigest.getInstance("SHA-256")
            .digest("zbx:00000000000000000005:3\n".toByteArray()).joinToString("") { "%02x".format(it) }
        assertEquals(expected, problemsChecksum(rows))
        val list = ProblemList()
        assertFalse(list.apply(ProblemsDelta(0, 1)))
        list.snapshot(5, rows)
        assertFalse(list.apply(ProblemsDelta(4, 6))) // does not continue: snapshot needed
        assertTrue(list.apply(ProblemsDelta(5, 6, remove = listOf(ProblemKey("zbx", "5")))))
        assertTrue(list.problems.isEmpty())
    }

    @Test
    fun ackTextLikeTheServer() {
        assertEquals("sto verificando", normalizeAckText("  sto\u0007 verificando \n"))
        assertEquals("a\nb", normalizeAckText("a\nb"))
        assertNull(normalizeAckText("   "))
        assertNull(normalizeAckText("x".repeat(1001)))
        assertEquals("é", normalizeAckText("é"))
    }

    @Test
    fun backoffGrowsWithJitterAndCaps() {
        val b = Backoff(random = { 1.0 })
        assertEquals(1000, b.next())
        assertEquals(2000, b.next())
        repeat(20) { b.next() }
        assertEquals(300_000, b.next())
        b.reset()
        assertEquals(1000, b.next())
    }

    @Test
    fun oldIsJudgedOnTheTimeOfTheMessage() {
        val now = 1_790_000_000_000L
        val problemStart = now - 3 * 3_600_000          // problem open for 3 hours
        val recoveryVersion = 3 * 10_000_000_000L + (now - 5_000) / 1000
        assertEquals(now - 5_000, Presentation.messageTimeMs(recoveryVersion))
        // The recovery just sent is notified, although the problem started 3 hours ago
        assertEquals(Presentation.Decision.Notify, Presentation.decide(now, Presentation.messageTimeMs(recoveryVersion), 0, true, true))
        assertTrue(Presentation.decide(now, problemStart, 0, true, true) is Presentation.Decision.Silent)
    }

    @Test
    fun listKeepsResolvedProblemsForHistory() {
        val now = isoMillis("2026-10-03T12:00:00Z")!!
        val list = ProblemList()
        list.snapshot(1, listOf(
            ProblemRow("zbx", "1", "open", "a", 4, "2026-10-03T08:00:00Z"),
            ProblemRow("zbx", "2", "resolved", "b", 4, "2026-10-03T08:00:00Z", rClock = "2026-10-03T11:00:00+02:00"),
            ProblemRow("zbx", "3", "resolved", "c", 4, "2026-10-01T08:00:00Z", rClock = "2026-10-02T11:00:00Z"),
            ProblemRow("zbx", "4", "gone", "d", 4, "2026-10-01T08:00:00Z"),
        ))
        assertTrue(list.problems.first { it.eventid == "2" }.isResolved)
        assertFalse(list.problems.first { it.eventid == "2" }.isOpen)
        // Resolved within 7 days: kept for History; gone without a time: removed at once
        assertEquals(listOf("4"), list.prune(now).map { it.eventid })
        assertEquals(listOf("3"), list.prune(now, 24 * 3600_000L).map { it.eventid })
        assertEquals(listOf("1", "2"), list.problems.map { it.eventid }.sorted())
        assertEquals(isoMillis("2026-10-03T09:00:00Z"), isoMillis("2026-10-03T11:00:00+02:00"))
        assertNull(isoMillis("not a time"))
    }
}
