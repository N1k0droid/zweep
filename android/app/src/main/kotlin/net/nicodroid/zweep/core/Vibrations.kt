// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

/**
 * Vibration of a channel. Each Zweep sound is a few notes with a rhythm of its own:
 * the Zweep vibration of a channel beats the same rhythm, so that a phone set to vibrate tells the
 * severity by itself. A pattern is what Android wants: milliseconds of pause and of vibration in
 * turn, the first pause being zero.
 */
object Vibrations {
    /** Longest custom rhythm, and how many taps it may have */
    const val MAX_MS = 3000L
    const val MAX_TAPS = 10

    // A motor does not render what is shorter than this: a tap or a pause is at least so long
    private const val MIN_ON = 40L
    private const val MIN_OFF = 40L
    private const val MAX_STEP = 1500L

    /** The rhythm of the Zweep sound of a channel: a severity, the recoveries, or the custom channels */
    fun zweep(channelId: String): LongArray = when (channelId) {
        "sev_5" -> longArrayOf(0, 300, 60, 300, 60, 300) // Disaster: three bursts
        "sev_4" -> longArrayOf(0, 90, 40, 110, 70, 90, 40, 110) // High: a double beep, twice
        "sev_3" -> longArrayOf(0, 80, 40, 80, 40, 200) // Average: three notes, the last one long
        "sev_2" -> longArrayOf(0, 90, 40, 200) // Warning: two notes
        "sev_1" -> longArrayOf(0, 200) // Information: one ping
        "sev_0" -> longArrayOf(0, 150) // Not classified: a blip
        "resolved" -> longArrayOf(0, 60, 40, 60, 40, 220) // three light notes rising
        else -> longArrayOf(0, 300) // custom channels: one struck note ringing out
    }

    /**
     * The rhythm a person tapped: for each tap the time the finger went down and up (ms, any clock).
     * Too short taps and pauses are stretched to what a motor renders, the rhythm is cut at [MAX_MS]
     * and [MAX_TAPS]. Null without taps.
     */
    fun fromTaps(taps: List<Pair<Long, Long>>): LongArray? {
        val sorted = taps.filter { it.second >= it.first }.sortedBy { it.first }.take(MAX_TAPS)
        if (sorted.isEmpty()) return null
        val out = mutableListOf(0L)
        var total = 0L
        for ((i, tap) in sorted.withIndex()) {
            if (i > 0) {
                val gap = (tap.first - sorted[i - 1].second).coerceIn(MIN_OFF, MAX_STEP)
                if (total + gap + MIN_ON > MAX_MS) break
                out += gap
                total += gap
            }
            val on = (tap.second - tap.first).coerceIn(MIN_ON, MAX_STEP).coerceAtMost(MAX_MS - total)
            out += on
            total += on
            if (total >= MAX_MS) break
        }
        return out.toLongArray()
    }

    /** Total length of a pattern, in ms */
    fun length(pattern: LongArray): Long = pattern.sum()
}
