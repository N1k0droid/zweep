// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

/**
 * Tracks the contiguous persisted position of one server stream (acked_seq). A message is counted
 * only after it is persisted; the position advances only over contiguous sequences, so the server
 * never considers delivered a message the device does not have.
 */
class SeqTracker(acked: Long) {
    var acked: Long = acked
        private set
    private val ahead = sortedSetOf<Long>()
    private var resyncFrom = -1L

    sealed interface Result {
        /** Already persisted earlier: acknowledge again, do not notify */
        data object Duplicate : Result

        /** New message; resyncFrom > 0 asks the server to resend from there (a hole before it) */
        data class New(val resyncFrom: Long) : Result
    }

    /** Called after the message with [seq] has been persisted */
    fun persisted(seq: Long): Result {
        if (seq <= acked || seq in ahead) return Result.Duplicate
        ahead += seq
        advance()
        val hole = ahead.isNotEmpty() && ahead.first() > acked + 1
        val from = if (hole && resyncFrom != acked + 1) acked + 1 else -1L
        if (from > 0) resyncFrom = from
        return Result.New(from)
    }

    /** Messages beyond the server retention: record the gap and move on (never silently) */
    fun gap(toSeq: Long) {
        if (toSeq > acked) {
            acked = toSeq
            ahead.removeAll { it <= acked }
            advance()
        }
    }

    /** A fresh install adopts the position the server holds for the device */
    fun adopt(seq: Long) {
        if (acked < 0) {
            acked = seq
            advance()
        }
    }

    private fun advance() {
        while (ahead.isNotEmpty() && ahead.first() == acked + 1) {
            acked = ahead.pollFirst()!!
        }
        if (resyncFrom in 1..acked) resyncFrom = -1
    }
}

/** Key under which the same alarm received from several servers is shown once */
fun dedupKey(source: String, eventId: String, kind: String, ver: Long) = "$source:$eventId:$kind:$ver"
