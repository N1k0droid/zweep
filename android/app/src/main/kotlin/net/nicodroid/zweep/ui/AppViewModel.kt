// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import net.nicodroid.zweep.core.ProblemRow
import net.nicodroid.zweep.core.WireJson
import net.nicodroid.zweep.data.AckEntity
import net.nicodroid.zweep.data.ChannelEntity
import net.nicodroid.zweep.data.MessageEntity
import net.nicodroid.zweep.data.ServerEntity
import net.nicodroid.zweep.data.SettingEntity
import net.nicodroid.zweep.data.Settings
import net.nicodroid.zweep.data.SourceEntity
import net.nicodroid.zweep.net.Detail
import net.nicodroid.zweep.service.DeliveryService
import net.nicodroid.zweep.service.Engine
import net.nicodroid.zweep.service.LinkState
import net.nicodroid.zweep.service.Reminders
import net.nicodroid.zweep.service.Revocations
import net.nicodroid.zweep.service.Updater

/** One event on the Notifications tab: the latest state of all its messages */
data class EventCard(
    val serverRef: Long,
    val serverLabel: String,
    val sid: String,
    val source: String,
    val sourceName: String,
    val eventId: String,
    val sev: Int,
    val title: String,
    val host: String,
    val name: String,
    val kind: String, // kind of the latest message: problem, update, recovery, test
    val acknowledged: Boolean,
    val firstAt: Long,
    val lastAt: Long,
    val unread: Boolean,
    val silenced: Boolean,
    val reminders: Int,
    val silentReason: String?,
    /** No longer needed on the Active view: resolved for a while, or a test already seen */
    val archived: Boolean = false,
    /** Custom channel of the alarm, if any: name and color (#RRGGBB, may be empty) */
    val channelName: String = "",
    val channelColor: String = "",
)

data class ProblemItem(val serverRef: Long, val serverLabel: String, val sourceName: String, val row: ProblemRow, val stale: Boolean)

/** A server that cannot be reached; asOf: time its data were last current */
data class HistoryNotice(val name: String, val asOf: Long?)

data class Filters(val minSeverity: Int = 0, val openOnly: Boolean = false, val query: String = "", val source: String? = null)

class AppViewModel(app: Application) : AndroidViewModel(app) {
    private val db = Engine.db

    /** null until the database answered: no server yet and not loaded yet are different things */
    val serversLoaded: StateFlow<List<ServerEntity>?> = db.servers().observe().stateIn(viewModelScope, SharingStarted.Eagerly, null)
    val servers: StateFlow<List<ServerEntity>> = serversLoaded.map { it.orEmpty() }.stateIn(viewModelScope, SharingStarted.Eagerly, emptyList())
    val links: StateFlow<Map<Long, LinkState>> = Engine.states
    val channels: StateFlow<List<ChannelEntity>> = db.channels().observe().stateIn(viewModelScope, SharingStarted.Eagerly, emptyList())
    val settings: StateFlow<Map<String, String>> = db.settings().observe().map { l -> l.associate { it.key to it.value } }
        .stateIn(viewModelScope, SharingStarted.Eagerly, emptyMap())

    /** App update offered by a server (null: none, or already installed) */
    val updateOffer: StateFlow<Updater.Offer?> = settings.map { Updater.parse(it[Updater.OFFER]) }
        .stateIn(viewModelScope, SharingStarted.Eagerly, null)
    val updateState: StateFlow<Updater.State> = Updater.state

    /** Times the update dialog appeared for a version */
    fun updatePrompts(code: Long): Int = settings.value[Updater.seenKey(code)]?.toIntOrNull() ?: 0
    fun updatePrompted(code: Long) = viewModelScope.launch { Updater.prompted(code) }
    fun startUpdate() = viewModelScope.launch { Updater.install(getApplication()) }
    fun resetUpdate() = Updater.reset()
    val updateConfirm: StateFlow<android.content.Intent?> = Updater.confirm
    fun openUpdateConfirmation(ctx: android.content.Context) = Updater.openConfirmation(ctx)

    /** Result of the manual check (Settings → Update); null: not checked yet */
    sealed interface UpdateCheck {
        data object Checking : UpdateCheck
        data object UpToDate : UpdateCheck
        data class Failed(val servers: String) : UpdateCheck
    }

    private val _updateCheck = MutableStateFlow<UpdateCheck?>(null)
    val updateCheck: StateFlow<UpdateCheck?> = _updateCheck

    /** Reads the configuration of every server again: it carries the update offered */
    fun checkUpdates() = viewModelScope.launch(Dispatchers.IO) {
        _updateCheck.value = UpdateCheck.Checking
        val failed = db.servers().all().filter { it.enabled }.filter { s -> runCatching { Engine.refreshConfig(s) }.isFailure }.map { it.label }
        _updateCheck.value = if (failed.isEmpty()) UpdateCheck.UpToDate else UpdateCheck.Failed(failed.joinToString())
    }

    /** Emits every minute: archiving depends on the time elapsed, not only on new messages */
    private val minuteTick = flow {
        while (true) {
            emit(System.currentTimeMillis())
            delay(60_000)
        }
    }

    val events: StateFlow<List<EventCard>> = combine(db.messages().observeRecent(2000), servers, settings, minuteTick, channels) { msgs, srv, set, _, chs ->
        val labels = srv.associate { it.id to it.label }
        val custom = chs.filter { it.kind == "custom" }.associateBy { it.serverRef to it.id }
        val resolvedMs = (set[Settings.RESOLVED_MINUTES]?.toLongOrNull() ?: Settings.DEFAULT_RESOLVED_MINUTES.toLong()) * 60_000L
        val now = System.currentTimeMillis()
        msgs.groupBy { it.serverRef to it.sid }.map { (_, list) ->
            val c = card(list, labels[list.first().serverRef] ?: "", now, resolvedMs)
            val ch = list.last().channels.split(',').firstNotNullOfOrNull { custom[c.serverRef to it] }
            if (ch == null) c else c.copy(channelName = ch.name, channelColor = ch.color)
        }.sortedByDescending { it.lastAt }
    }.stateIn(viewModelScope, SharingStarted.Eagerly, emptyList())

    val problems: StateFlow<List<ProblemItem>> = combine(db.problems().observe(), db.sources().observe(), servers) { rows, sources, srv ->
        val labels = srv.associate { it.id to it.label }
        val src = sources.associateBy { it.serverRef to it.id }
        rows.mapNotNull { e ->
            val row = runCatching { WireJson.decodeFromString(ProblemRow.serializer(), e.json) }.getOrNull() ?: return@mapNotNull null
            val s = src[e.serverRef to e.source]
            ProblemItem(e.serverRef, labels[e.serverRef] ?: "", s?.name ?: e.source, row, s?.stale ?: false)
        }
    }.stateIn(viewModelScope, SharingStarted.Eagerly, emptyList())

    /** Time (ms) the data of each server were last current: shown when it becomes unreachable */
    val liveUntil: StateFlow<Map<Long, Long>> = settings.map { m ->
        m.filterKeys { it.startsWith(Settings.LIVE_UNTIL) }.mapNotNull { (k, v) ->
            val id = k.removePrefix(Settings.LIVE_UNTIL).toLongOrNull() ?: return@mapNotNull null
            v.toLongOrNull()?.let { id to it }
        }.toMap()
    }.stateIn(viewModelScope, SharingStarted.Eagerly, emptyMap())

    /** At least one server offers the Recent and History views */
    val hasProblemViews: StateFlow<Boolean> = servers.map { l -> l.any { "problem_views" in it.features.split(',') } }
        .stateIn(viewModelScope, SharingStarted.Eagerly, false)

    val hasProblems: StateFlow<Boolean> = servers.map { l -> l.any { "problems" in it.features.split(',') } }
        .stateIn(viewModelScope, SharingStarted.Eagerly, false)

    private fun card(list: List<MessageEntity>, label: String, now: Long, resolvedMs: Long): EventCard {
        val sorted = list.sortedBy { it.ver }
        val first = sorted.first()
        val last = sorted.last()
        val acked = sorted.any { it.body.contains("\"acknowledged\":true") || it.body.contains("\"action\":\"acknowledged\"") }
        return EventCard(
            serverRef = first.serverRef, serverLabel = label, sid = first.sid, source = first.source, sourceName = first.sourceName,
            eventId = first.eventId, sev = sorted.maxOf { it.sev }, title = first.title, host = first.host, name = first.name.ifEmpty { first.title },
            kind = last.kind, acknowledged = acked, firstAt = first.eventTime.takeIf { it > 0 } ?: first.receivedAt,
            lastAt = last.receivedAt, unread = sorted.any { it.readAt == 0L }, silenced = sorted.any { it.silenced },
            reminders = sorted.maxOf { it.reminders }, silentReason = last.reason.takeIf { last.state == MessageEntity.STATE_SILENT },
            archived = (last.kind == "recovery" && now - last.receivedAt > resolvedMs) ||
                (first.kind == "test" && (sorted.none { it.readAt == 0L } || now - last.receivedAt > Settings.TEST_ARCHIVE_MS)),
        )
    }

    fun history(serverRef: Long, sid: String, onResult: (List<MessageEntity>) -> Unit) = viewModelScope.launch {
        onResult(db.messages().history(serverRef, sid))
    }

    fun markRead(serverRef: Long, sid: String) = viewModelScope.launch {
        db.messages().markRead(serverRef, sid, System.currentTimeMillis())
    }

    fun markAllRead() = viewModelScope.launch { db.messages().markAllRead(System.currentTimeMillis()) }


    fun acksFor(serverRef: Long, source: String, eventid: String): Flow<List<AckEntity>> = db.acks().observeFor(serverRef, source, eventid)

    fun sendAck(serverRef: Long, source: String, eventid: String, text: String) = viewModelScope.launch {
        Engine.queueAck(serverRef, source, eventid, text)
    }

    suspend fun detail(serverRef: Long, source: String, eventid: String): Result<Detail> = withContext(Dispatchers.IO) {
        runCatching {
            val s = db.servers().get(serverRef) ?: error("server")
            Engine.api(s).detail(source, eventid)
        }
    }

    suspend fun source(serverRef: Long, source: String): SourceEntity? = db.sources().get(serverRef, source)

    fun refreshProblems() = viewModelScope.launch(Dispatchers.IO) {
        db.servers().all().filter { "problems" in it.features.split(',') }.forEach { runCatching { Engine.fetchSnapshot(it) } }
    }

    fun setDnd(untilMs: Long) = viewModelScope.launch { if (untilMs > 0) Engine.setDnd(untilMs) else Engine.dndEnded() }

    /** Severities shown by a list (saved per list); all by default */
    fun severities(settings: Map<String, String>, key: String): Set<Int> =
        settings[key]?.split(',')?.mapNotNull { it.toIntOrNull() }?.toSet() ?: (0..5).toSet()

    fun setSeverities(key: String, sev: Set<Int>) = viewModelScope.launch {
        db.settings().put(net.nicodroid.zweep.data.SettingEntity(key, sev.sorted().joinToString(",")))
    }

    fun setSetting(key: String, value: String) = viewModelScope.launch {
        db.settings().put(net.nicodroid.zweep.data.SettingEntity(key, value))
    }

    fun setHistoryMinutes(minutes: Int) = viewModelScope.launch(Dispatchers.IO) {
        Engine.settings.setHistoryMinutes(minutes)
        Engine.purgeHistory()
    }

    fun setMaxServers(n: Int) = viewModelScope.launch { Engine.settings.setMaxServers(n) }

    fun updateChannel(c: ChannelEntity) = viewModelScope.launch { db.channels().upsert(listOf(c)) }

    fun testAlarm(serverRef: Long, severity: Int, onResult: (Boolean) -> Unit) = viewModelScope.launch(Dispatchers.IO) {
        val ok = runCatching { Engine.api(db.servers().get(serverRef)!!).testAlarm(severity) }.isSuccess
        withContext(Dispatchers.Main) { onResult(ok) }
    }

    /** The admin allowed this user to force-close alerts on that server */
    fun canClose(serverRef: Long): Boolean = servers.value.any { it.id == serverRef && "close" in it.features.split(',') }

    /** Zabbix still lists the problem as open (the Problems tab of that server) */
    fun zabbixOpen(e: EventCard): Boolean = problems.value.any { it.row.isOpen && it.serverRef == e.serverRef && it.row.source == e.source && it.row.eventid == e.eventId }

    /** Forced close for every recipient; the recovery arrives on the stream. onResult gets null or an error code */
    fun closeAlert(serverRef: Long, sid: String, onResult: (String?) -> Unit) = viewModelScope.launch(Dispatchers.IO) {
        val err = runCatching { Engine.api(db.servers().get(serverRef)!!).closeAlert(sid) }.exceptionOrNull()
        val code = when (err) {
            null -> null
            is net.nicodroid.zweep.net.ApiException -> err.code
            else -> "network"
        }
        withContext(Dispatchers.Main) { onResult(code) }
    }

    fun logout(serverRef: Long) = viewModelScope.launch(Dispatchers.IO) { Engine.logout(serverRef) }

    fun quit(onDone: () -> Unit) = viewModelScope.launch {
        Engine.quit()
        Engine.settings.setQuit(true)
        DeliveryService.stop(getApplication())
        onDone()
    }

    fun resume() = viewModelScope.launch {
        if (Engine.settings.quit()) Engine.settings.setQuit(false)
        if (db.servers().all().isNotEmpty()) DeliveryService.start(getApplication())
        Engine.sendStatus()
        if (Revocations.retry()) Reminders.watchdog(getApplication()) // permissions may have changed in the system settings
    }

    fun enroll(
        url: String, code: String?, username: String?, password: String?, pins: List<String>, cleartext: Boolean,
        onResult: (Engine.EnrollResult) -> Unit,
    ) = viewModelScope.launch(Dispatchers.IO) {
        val r = Engine.enroll(url, code, username, password, pins, cleartext)
        withContext(Dispatchers.Main) { onResult(r) }
    }
}
