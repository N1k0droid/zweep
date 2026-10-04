// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.data

/** App settings (key/value in the device-protected database, so they are readable in Direct Boot) */
class Settings(private val dao: SettingDao) {
    suspend fun maxServers(): Int = dao.get(MAX_SERVERS)?.toIntOrNull()?.coerceIn(1, LIMIT_SERVERS) ?: 1
    suspend fun setMaxServers(n: Int) = dao.put(SettingEntity(MAX_SERVERS, n.coerceIn(1, LIMIT_SERVERS).toString()))

    suspend fun dndUntil(): Long = dao.get(DND_UNTIL)?.toLongOrNull() ?: 0
    suspend fun setDndUntil(ms: Long) = dao.put(SettingEntity(DND_UNTIL, ms.toString()))

    /** Start of the current Do Not Disturb, to summarize what arrived meanwhile */
    suspend fun dndSince(): Long = dao.get(DND_SINCE)?.toLongOrNull() ?: 0
    suspend fun setDndSince(ms: Long) = dao.put(SettingEntity(DND_SINCE, ms.toString()))

    suspend fun oldThresholdMinutes(): Int = dao.get(OLD_MINUTES)?.toIntOrNull() ?: 60
    /** Local history (alarms kept on the phone), minutes; a choice in days of older builds is converted */
    suspend fun historyMinutes(): Int = localHistoryMinutes(dao.get(HISTORY_MINUTES), dao.get(HISTORY_DAYS))
    suspend fun setHistoryMinutes(m: Int) = dao.put(SettingEntity(HISTORY_MINUTES, m.toString()))

    suspend fun onboarded(): Boolean = dao.get(ONBOARDED) == "1"
    suspend fun setOnboarded() = dao.put(SettingEntity(ONBOARDED, "1"))

    /** The user chose "Quit app": the service stays stopped until the app is opened again */
    suspend fun quit(): Boolean = dao.get(QUIT) == "1"
    suspend fun setQuit(q: Boolean) = dao.put(SettingEntity(QUIT, if (q) "1" else "0"))

    /** Device tokens whose revocation did not reach the server yet (JSON, tokens sealed by the Keystore) */
    suspend fun pendingRevocations(): String? = dao.get(PENDING_REVOCATIONS)
    suspend fun setPendingRevocations(json: String) = dao.put(SettingEntity(PENDING_REVOCATIONS, json))

    companion object {
        const val PENDING_REVOCATIONS = "pending_revocations"
        const val LIMIT_SERVERS = 5
        const val MAX_SERVERS = "max_servers"
        const val DND_UNTIL = "dnd_until"
        const val DND_SINCE = "dnd_since"
        const val OLD_MINUTES = "old_minutes"
        /** Days of the Local history of older builds (1, 7, 30): read once as minutes, at most 7 days */
        const val HISTORY_DAYS = "history_days"
        const val HISTORY_MINUTES = "history_minutes"
        const val DEFAULT_HISTORY_MINUTES = 10080

        fun localHistoryMinutes(minutes: String?, oldDays: String?): Int =
            (minutes?.toIntOrNull() ?: oldDays?.toIntOrNull()?.let { it * 1440 } ?: DEFAULT_HISTORY_MINUTES)
                .coerceAtMost(problemHistoryChoicesMinutes.last())
        const val RESOLVED_MINUTES = "resolved_minutes"
        const val NOTIF_VIEW = "notif_view"
        const val DEFAULT_RESOLVED_MINUTES = 60
        const val TEST_ARCHIVE_MS = 10 * 60_000L
        val resolvedChoicesMinutes = listOf(5, 60, 1440)
        /** Period of the History view of the Problems tab (minutes) */
        const val PROBLEM_HISTORY_MINUTES = "problem_history_minutes"
        /** Prefix of the History copies of 0.9.10 to 1.0.0 pre-releases, removed at start */
        const val OLD_HISTORY_CACHE = "history_cache."
        /** Prefix of the time (ms) the data of a server were last known to be current (key + server id) */
        const val LIVE_UNTIL = "live_until."
        const val DEFAULT_PROBLEM_HISTORY_MINUTES = 1440
        val problemHistoryChoicesMinutes = listOf(60, 180, 720, 1440, 10080)

        /** Period of History in minutes: the stored choice, at most 7 days (the list keeps no more) */
        fun problemHistoryMinutes(stored: String?): Int =
            (stored?.toIntOrNull() ?: DEFAULT_PROBLEM_HISTORY_MINUTES).coerceAtMost(problemHistoryChoicesMinutes.last())
        const val ONBOARDED = "onboarded"
        const val QUIT = "quit"
        val dndChoicesMinutes = listOf(30, 60, 120)
    }
}
