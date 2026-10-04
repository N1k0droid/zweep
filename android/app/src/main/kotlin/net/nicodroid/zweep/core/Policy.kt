// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

/** Zabbix severities 0..5 */
object Severity {
    const val NOT_CLASSIFIED = 0
    const val INFORMATION = 1
    const val WARNING = 2
    const val AVERAGE = 3
    const val HIGH = 4
    const val DISASTER = 5
    val all = 0..5

    fun label(sev: Int) = when (sev) {
        0 -> "NOT CLASSIFIED"
        1 -> "INFORMATION"
        2 -> "WARNING"
        3 -> "AVERAGE"
        4 -> "HIGH"
        else -> "DISASTER"
    }
}

const val INFINITE = -1

/** Per-channel notification preferences */
data class ChannelPrefs(
    val notify: Boolean = true,
    val reminders: Int = 0, // 0, 1, 3, 5 or INFINITE
    val reminderMinutes: Int = 2,
) {
    companion object {
        fun defaultsFor(severity: Int?) = when (severity) {
            Severity.DISASTER -> ChannelPrefs(reminders = INFINITE)
            Severity.HIGH -> ChannelPrefs(reminders = 3)
            Severity.AVERAGE -> ChannelPrefs(reminders = 1)
            else -> ChannelPrefs(reminders = 0)
        }
        val reminderChoices = listOf(0, 1, 3, 5, INFINITE)
        val intervalChoices = listOf(1, 2, 5, 10, 15)
    }

    fun remindAgain(done: Int) = reminders == INFINITE || done < reminders
}

/**
 * Paces notify() calls: Android drops posts silently above ~5/s (measured).
 * Returns the delay before the next post is allowed.
 */
class Pacer(private val perSecond: Int = 4) {
    private val recent = ArrayDeque<Long>()

    fun delayFor(nowMs: Long): Long {
        while (recent.isNotEmpty() && nowMs - recent.first() >= 1000) recent.removeFirst()
        return if (recent.size < perSecond) 0 else 1000 - (nowMs - recent.first())
    }

    fun posted(nowMs: Long) {
        recent.addLast(nowMs)
    }
}

/** An alarm notification currently shown */
data class Shown(val key: String, val severity: Int, val resolved: Boolean, val postedAt: Long)

/**
 * Notifications the app keeps on screen. Android silently drops what an app posts beyond its limit:
 * 50 on AOSP, 25 on Samsung One UI (posted plus still enqueued). [SAFE_TOTAL] is below every known
 * limit with a margin for cancels still in flight; it counts every notification of the app (service,
 * system notices, group summaries), the alarms get what is left. When full, a new alarm replaces:
 * resolved ones first, then lower severities, then the oldest. An unresolved alarm is never removed
 * for a less severe one: in that case the new alarm is only stored.
 */
object Rotation {
    const val SAFE_TOTAL = 20

    /** Room for alarms when [others] notifications of the app are not alarms */
    fun alarmCap(others: Int) = (SAFE_TOTAL - others).coerceAtLeast(1)

    /** The alarms to remove so that one more fits under [cap]; null when not possible */
    fun victims(shown: List<Shown>, newSeverity: Int, cap: Int): List<Shown>? {
        val need = shown.size - cap + 1
        if (need <= 0) return emptyList()
        val candidates = shown.filter { it.resolved }.sortedBy { it.postedAt } +
            shown.filter { !it.resolved && it.severity <= newSeverity }.sortedWith(compareBy<Shown>({ it.severity }, { it.postedAt }))
        return if (candidates.size >= need) candidates.take(need) else null
    }

    fun victim(shown: List<Shown>, newSeverity: Int, cap: Int): Shown? = victims(shown, newSeverity, cap)?.firstOrNull()

    fun hasRoom(shown: List<Shown>, newSeverity: Int, cap: Int) = victims(shown, newSeverity, cap) != null
}

/** Why a message was stored without a notification (receipt reason not_shown) */
object NotShownReason {
    const val APP_DND = "app_dnd"
    const val OLD = "old"
    const val CHANNEL_OFF = "channel_off"
    const val CAP = "cap"
    const val PERMISSION = "permission" // notifications of the app turned off
    const val OS_LIMIT = "os_limit" // Android refused it: too many notifications of the app
    const val NOT_DISPLAYED = "not_displayed" // posted, but Android did not show it
    const val FILTER = "filter"
    const val SILENCED = "silenced" // the user silenced this alarm: updates and recovery arrive without sound
}

/**
 * Decides how a newly persisted alarm is presented. Order of the rules: app Do Not Disturb
 * (nothing sounds), old messages (stored silently, summarized), channel preferences.
 */
object Presentation {
    /**
     * Time of the message itself (problem start, update, recovery or forced close), in ms: the version
     * of a message is kind rank × 10^10 + that time in seconds. "Old" is judged on this time, not on the
     * start of the problem: the recovery of a problem open for hours is fresh news.
     */
    fun messageTimeMs(version: Long): Long = (version % 10_000_000_000L) * 1000

    sealed interface Decision {
        data object Notify : Decision
        data class Silent(val reason: String) : Decision
    }

    fun decide(
        nowMs: Long,
        eventTimeMs: Long,
        dndUntilMs: Long,
        channelNotify: Boolean,
        notificationsAllowed: Boolean,
        oldThresholdMs: Long = 3_600_000,
    ): Decision = when {
        dndUntilMs > nowMs -> Decision.Silent(NotShownReason.APP_DND)
        eventTimeMs > 0 && nowMs - eventTimeMs > oldThresholdMs -> Decision.Silent(NotShownReason.OLD)
        !channelNotify -> Decision.Silent(NotShownReason.CHANNEL_OFF)
        !notificationsAllowed -> Decision.Silent(NotShownReason.PERMISSION)
        else -> Decision.Notify
    }
}

/** Reconnection backoff with jitter: 1 s doubling up to 5 min */
class Backoff(private val baseMs: Long = 1_000, private val maxMs: Long = 300_000, private val random: () -> Double = Math::random) {
    private var attempt = 0

    fun next(): Long {
        val exp = (baseMs shl attempt.coerceAtMost(20)).coerceAtMost(maxMs)
        attempt++
        return (exp / 2 + (exp / 2 * random()).toLong()).coerceAtLeast(baseMs / 2)
    }

    fun reset() {
        attempt = 0
    }
}
