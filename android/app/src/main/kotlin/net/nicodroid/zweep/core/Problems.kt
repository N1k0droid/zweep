// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

import java.security.MessageDigest
import java.text.Normalizer

/** Checksum of a problem list, identical to the server (internal/api.Checksum) */
fun problemsChecksum(rows: Collection<ProblemRow>): String {
    val keys = rows.map { "%s:%020d:%d".format(it.source, it.eventid.toLong(), it.version) }.sorted()
    val md = MessageDigest.getInstance("SHA-256")
    for (k in keys) {
        md.update(k.toByteArray())
        md.update('\n'.code.toByte())
    }
    return "sha256:" + md.digest().joinToString("") { "%02x".format(it) }
}

/** The problem list of one server: a snapshot plus the deltas that follow it */
class ProblemList {
    var rev: Long = 0
        private set
    private val rows = linkedMapOf<String, ProblemRow>()

    val problems: List<ProblemRow> get() = rows.values.toList()

    fun snapshot(rev: Long, list: List<ProblemRow>) {
        rows.clear()
        list.forEach { rows[key(it.source, it.eventid)] = it }
        this.rev = rev
    }

    /** Applies a delta; false means it does not continue this list (a new snapshot is needed) */
    fun apply(delta: ProblemsDelta): Boolean {
        if (rev == 0L || delta.fromRev != rev) return false
        delta.upsert.forEach { rows[key(it.source, it.eventid)] = it }
        delta.remove.forEach { rows.remove(key(it.source, it.eventid)) }
        rev = delta.toRev
        return true
    }

    fun checksum() = problemsChecksum(rows.values)

    /**
     * Drops the resolved (or gone) problems older than the longest History period; returns their keys.
     * The server sends resolved problems in the deltas, but never their expiry.
     */
    fun prune(nowMs: Long, keepMs: Long = LIST_KEEP_MS): List<ProblemKey> {
        val old = rows.values.filter { !it.isOpen && (isoMillis(it.rClock) ?: 0L) < nowMs - keepMs }
        old.forEach { rows.remove(key(it.source, it.eventid)) }
        return old.map { ProblemKey(it.source, it.eventid) }
    }

    private fun key(source: String, eventid: String) = "$source:$eventid"
}

/** Resolved problems are kept this long: the longest History period (and the server keeps them as long) */
const val LIST_KEEP_MS = 7 * 24 * 3600_000L

/** Milliseconds of an RFC 3339 time of the server, null if absent or invalid */
fun isoMillis(s: String?): Long? = s?.let { runCatching { java.time.OffsetDateTime.parse(it).toInstant().toEpochMilli() }.getOrNull() }

/**
 * Normalizes the text of an ack like the server: NFC, control characters removed except newlines,
 * trimmed, 1..1000 characters. Returns null if the text is not acceptable.
 */
fun normalizeAckText(input: String): String? {
    val nfc = Normalizer.normalize(input, Normalizer.Form.NFC)
    val cleaned = buildString {
        nfc.forEach { c -> if (c == '\n' || !Character.isISOControl(c)) append(c) }
    }.trim()
    val runes = cleaned.codePointCount(0, cleaned.length)
    return if (runes in 1..1000) cleaned else null
}
