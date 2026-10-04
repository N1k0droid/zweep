// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.content.Context
import android.os.Build
import android.provider.Settings as AndroidSettings
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import net.nicodroid.zweep.BuildConfig
import net.nicodroid.zweep.R
import net.nicodroid.zweep.core.ChannelPrefs
import net.nicodroid.zweep.core.DeviceInfo
import net.nicodroid.zweep.core.Frames
import net.nicodroid.zweep.core.Incoming
import net.nicodroid.zweep.core.MsgFrame
import net.nicodroid.zweep.core.NotShownReason
import net.nicodroid.zweep.core.Pacer
import net.nicodroid.zweep.core.Presentation
import net.nicodroid.zweep.core.ProblemList
import net.nicodroid.zweep.core.ProblemRow
import net.nicodroid.zweep.core.LIST_KEEP_MS
import net.nicodroid.zweep.core.ReceiptState
import net.nicodroid.zweep.core.SeqTracker
import net.nicodroid.zweep.core.WireJson
import net.nicodroid.zweep.core.dedupKey
import net.nicodroid.zweep.data.AckEntity
import net.nicodroid.zweep.data.ChannelEntity
import net.nicodroid.zweep.data.MessageEntity
import net.nicodroid.zweep.data.ProblemEntity
import net.nicodroid.zweep.data.ReceiptEntity
import net.nicodroid.zweep.data.ServerEntity
import net.nicodroid.zweep.data.SettingEntity
import net.nicodroid.zweep.data.Settings
import net.nicodroid.zweep.data.SourceEntity
import net.nicodroid.zweep.data.Vault
import net.nicodroid.zweep.data.ZweepDb
import net.nicodroid.zweep.net.Api
import net.nicodroid.zweep.net.ApiException
import net.nicodroid.zweep.net.Link
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap

data class LinkState(val state: ConnState, val detail: String?)

/** Coordinates the servers: connections, persistence, presentation, receipts, problems and acks */
object Engine {
    private lateinit var app: Context
    lateinit var db: ZweepDb
        private set
    lateinit var settings: Settings
        private set
    lateinit var notifier: Notifier
        private set

    /** Context for user-visible texts, in the language chosen in the app */
    private val text: Context get() = net.nicodroid.zweep.ui.Lang.wrap(app)

    val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val conns = ConcurrentHashMap<Long, Connection>()
    private val trackers = ConcurrentHashMap<Long, SeqTracker>()
    private val lists = ConcurrentHashMap<Long, ProblemList>()
    private val presentQueue = Channel<Unit>(Channel.CONFLATED)
    private val pacer = Pacer()
    private val serverLock = Mutex()
    private val _states = MutableStateFlow<Map<Long, LinkState>>(emptyMap())
    val states: StateFlow<Map<Long, LinkState>> = _states
    @Volatile private var started = false

    /** Do Not Disturb end, kept in memory for the persistent notification (never read on the main thread) */
    @Volatile var dndUntil = 0L
        private set

    fun init(ctx: Context) {
        if (::app.isInitialized) return
        app = ctx.applicationContext
        db = ZweepDb.get(app)
        settings = Settings(db.settings())
        notifier = Notifier(net.nicodroid.zweep.ui.Lang.wrap(app))
        notifier.ensureBaseChannels()
        scope.launch { dndUntil = settings.dndUntil() }
        scope.launch { upgradeProblemList() }
        scope.launch { for (u in presentQueue) runCatching { presentPending() }.onFailure { warn("present", it) } }
    }

    /**
     * The problem list now keeps the resolved problems of 7 days (History): a list made with a shorter
     * window is fetched again once, as a new snapshot; the History copies of the pre-releases go.
     */
    private suspend fun upgradeProblemList() {
        val dao = db.settings()
        if (dao.get(LIST_WINDOW) == LIST_KEEP_MS.toString()) return
        db.servers().all().forEach { db.servers().setProjRev(it.id, 0) }
        lists.clear()
        dao.deletePrefix(Settings.OLD_HISTORY_CACHE)
        dao.put(SettingEntity(LIST_WINDOW, LIST_KEEP_MS.toString()))
    }

    private const val LIST_WINDOW = "problem_list_window_ms"

    // ---------------------------------------------------------------- lifecycle

    /** Starts a connection for every enabled server and stops the others */
    suspend fun reconcile() = serverLock.withLock {
        started = true
        val servers = db.servers().all()
        val ids = servers.filter { it.enabled }.map { it.id }.toSet()
        conns.keys.filter { it !in ids }.forEach { conns.remove(it)?.stop() }
        for (s in servers.filter { it.enabled }) {
            trackers.getOrPut(s.id) { SeqTracker(s.ackedSeq) }
            conns.getOrPut(s.id) { newConnection(s.id) }.start()
        }
        presentQueue.trySend(Unit)
        Reminders.schedule(app)
    }

    fun stopAll() {
        started = false
        conns.values.forEach { it.stop() }
        conns.clear()
    }

    /** Quit app: the server records stopped_by_user; escalation in Zabbix continues */
    fun quit() {
        conns.values.forEach { it.send(Frames.bye("user_quit")) }
        stopAll()
    }

    fun pingAll() {
        val now = System.currentTimeMillis()
        conns.values.forEach { it.ping(now) }
    }

    fun networkAvailable() = conns.values.forEach { it.kick() }

    fun connectedCount() = conns.values.count { it.state == ConnState.CONNECTED }
    fun serverCount() = conns.size

    private fun newConnection(ref: Long): Connection = Connection(
        serverRef = ref,
        scope = scope,
        links = { runBlockingServer(ref)?.let { linksOf(it) } ?: emptyList() },
        token = { runBlockingServer(ref)?.let { runCatching { Vault.open(it.tokenEnc) }.getOrNull() } },
        handler = { c, f -> onFrame(ref, c, f) },
        onOpen = { c -> onOpen(ref, c) },
        onState = { id, s, d ->
            val was = _states.value[id]?.state
            _states.value = _states.value + (id to LinkState(s, d))
            // Leaving the connected state: the data of this server are current up to now
            if (was == ConnState.CONNECTED && s != ConnState.CONNECTED) scope.launch { markLive(id, force = true) }
            DeliveryService.refresh(app)
            // Revoked while the app was not connected: the server refuses the token at connect
            if (s == ConnState.REJECTED && d == "unauthorized") scope.launch { db.servers().get(ref)?.let { notifyRevoked(it) } }
        },
    )

    private val liveWritten = java.util.concurrent.ConcurrentHashMap<Long, Long>()

    /**
     * Records that the data of a server are current now (shown as "data of hh:mm" when it becomes
     * unreachable). Written at most once a minute while frames arrive, and when the connection drops.
     */
    private suspend fun markLive(ref: Long, force: Boolean = false) {
        val now = System.currentTimeMillis()
        if (!force && now - (liveWritten[ref] ?: 0L) < 60_000L) return
        liveWritten[ref] = now
        db.settings().put(SettingEntity(Settings.LIVE_UNTIL + ref, now.toString()))
    }

    /** Tells the user that a server revoked this device and how to reconfigure the app (one notice per server) */
    private fun notifyRevoked(s: ServerEntity) {
        notifier.system(Notifier.ID_REVOKED_BASE + s.id.toInt(), text.getString(R.string.sys_revoked_title, s.label), text.getString(R.string.sys_revoked_text, s.label))
    }

    private fun runBlockingServer(ref: Long): ServerEntity? = kotlinx.coroutines.runBlocking { db.servers().get(ref) }

    fun linksOf(s: ServerEntity): List<Link> {
        val pins = s.pins.split(',').map { it.trim() }.filter { it.isNotEmpty() }
        val urls = (listOf(s.baseUrl) + s.urls.split('\n')).map { it.trim().trimEnd('/') }.filter { it.isNotEmpty() }.distinct()
        return urls.map { Link(it, pins, s.cleartextAccepted) }
    }

    fun api(s: ServerEntity): Api = Api(linksOf(s).first(), Vault.open(s.tokenEnc))

    // ---------------------------------------------------------------- stream

    private suspend fun onOpen(ref: Long, c: Connection) {
        val s = db.servers().get(ref) ?: return
        c.send(Frames.hello(s.ackedSeq, s.projRev, deviceInfo(), Permissions.json(app), settings.dndUntil().takeIf { it > 0 }, System.currentTimeMillis()))
        runCatching { refreshConfig(s) }.onFailure { warn("config", it) }
        flushReceipts(ref, c)
        sendQueuedAcks(ref)
    }

    private suspend fun onFrame(ref: Long, c: Connection, f: Incoming) {
        val s = db.servers().get(ref) ?: return
        markLive(ref)
        val tracker = trackers.getOrPut(ref) { SeqTracker(s.ackedSeq) }
        when (f) {
            is Incoming.Hello -> {
                val w = f.welcome
                if (w.serverId.isNotEmpty() && w.serverId != s.serverId) {
                    // The address now answers with another Zweep server: never mix alarms
                    c.reject("server identity changed")
                    notifier.system(Notifier.ID_REVOKED, text.getString(R.string.sys_identity_title), text.getString(R.string.sys_identity_text, s.label))
                    return
                }
                tracker.adopt(w.startSeq)
                db.servers().advance(ref, tracker.acked)
                db.servers().setWelcome(ref, w.features.joinToString(","), w.nodeId)
                if ("problems" in w.features) {
                    if (s.projRev == 0L || lists[ref]?.rev == 0L) runCatching { fetchSnapshot(s) }.onFailure { warn("snapshot", it) }
                } else {
                    db.problems().deleteServer(ref)
                }
            }
            is Incoming.Msg -> onMessage(s, c, tracker, f.msg)
            is Incoming.Hole -> {
                tracker.gap(f.gap.toSeq)
                db.servers().advance(ref, tracker.acked)
                c.send(Frames.ack("", 0, ReceiptState.DELIVERED, null, tracker.acked))
                notifier.system(Notifier.ID_GAP, text.getString(R.string.sys_gap_title), text.getString(R.string.sys_gap_text, f.gap.toSeq - f.gap.fromSeq + 1, s.label))
            }
            is Incoming.Note -> when (f.notice.code) {
                "token_revoked" -> {
                    db.servers().setEnabled(ref, false)
                    c.reject("revoked")
                    notifyRevoked(s)
                }
                "config_changed" -> runCatching {
                    refreshConfig(s)
                    // Perimeter or source changes alter the user's problem list without a new revision
                    db.servers().get(ref)?.takeIf { "problems" in it.features.split(',') }?.let { fetchSnapshot(it) }
                }.onFailure { warn("config", it) }
                "token_rotate" -> runCatching { rotateToken(s) }.onFailure { warn("rotate", it) }
            }
            is Incoming.Delta -> onDelta(s, f)
            is Incoming.Stale -> db.sources().setStale(ref, f.stale.source, f.stale.stale)
            is Incoming.AckDone -> {
                val r = f.result
                db.acks().setState(r.requestId, r.state, r.reason, System.currentTimeMillis())
                if (r.state == AckEntity.REJECTED) {
                    notifier.system(Notifier.ID_ACK, text.getString(R.string.sys_ack_rejected_title), text.getString(R.string.sys_ack_rejected_text, r.reason ?: "-"))
                }
            }
            is Incoming.Pong, is Incoming.Unknown -> Unit
        }
    }

    /** Persist, then (asynchronously) notify, then confirm: the receipt never precedes the commit */
    private suspend fun onMessage(s: ServerEntity, c: Connection, tracker: SeqTracker, m: MsgFrame) {
        val b = m.body
        val eventId = b.str("event_id").ifEmpty { m.sid.substringAfterLast(':') }
        val key = dedupKey(m.source, eventId, m.kind, m.ver)
        val entity = MessageEntity(
            serverRef = s.id, seq = m.seq, msgId = m.id, sid = m.sid, ver = m.ver, kind = m.kind, sev = m.sev,
            channels = m.channels.joinToString(","), source = m.source, sourceName = b.str("source_name").ifEmpty { m.source },
            eventId = eventId, host = b.str("host"), name = b.str("name"), title = m.title, body = b.toString(),
            eventTime = (b["event_time"]?.jsonPrimitive?.longOrNull ?: m.ts) * 1000, receivedAt = System.currentTimeMillis(),
            dedupKey = key, duplicate = db.messages().seenElsewhere(key, s.id), notifId = Notifier.notifId(s.id, m.sid),
        )
        val inserted = db.messages().insert(entity) != -1L
        val result = tracker.persisted(m.seq)
        db.servers().advance(s.id, tracker.acked)
        if (inserted && entity.duplicate) {
            db.messages().setPresented(s.id, m.seq, MessageEntity.STATE_SILENT, "duplicate", entity.notifId, 0)
            db.receipts().insert(ReceiptEntity(serverRef = s.id, msgId = m.id, seq = m.seq, state = ReceiptState.NOT_SHOWN, reason = "duplicate"))
        } else if (inserted) {
            presentQueue.trySend(Unit)
        }
        c.send(Frames.ack(m.id, m.seq, ReceiptState.DELIVERED, null, tracker.acked))
        if (result is SeqTracker.Result.New && result.resyncFrom > 0) c.send(Frames.resync(result.resyncFrom))
    }

    private fun JsonObject.str(k: String) = runCatching { this[k]?.jsonPrimitive?.content }.getOrNull() ?: ""

    private suspend fun flushReceipts(ref: Long, c: Connection) {
        val s = db.servers().get(ref) ?: return
        val pending = db.receipts().pending(ref)
        val sent = pending.filter { c.send(Frames.ack(it.msgId, it.seq, it.state, it.reason, s.ackedSeq)) }.map { it.id }
        if (sent.isNotEmpty()) db.receipts().delete(sent)
    }

    // ---------------------------------------------------------------- presentation

    private suspend fun presentPending() {
        var offline = 0
        for (m in db.messages().pendingPresentation()) {
            val s = db.servers().get(m.serverRef) ?: continue
            val chId = m.channels.split(',').firstOrNull { it.isNotEmpty() && !it.startsWith("sev_") }
                ?: m.channels.split(',').firstOrNull { it.isNotEmpty() } ?: "sev_${m.sev}"
            val ch = db.channels().get(s.id, chId)
            val prefs = ch?.let { ChannelPrefs(it.notify && it.serverEnabled, it.reminders, it.reminderMinutes) } ?: ChannelPrefs.defaultsFor(m.sev)
            val now = System.currentTimeMillis()
            val sentAt = Presentation.messageTimeMs(m.ver).takeIf { it > 0 } ?: m.eventTime
            val decision = Presentation.decide(now, sentAt, settings.dndUntil(), prefs.notify,
                notifier.notificationsEnabled() && !notifier.channelBlocked(s.id, chId), settings.oldThresholdMinutes() * 60_000L)
            if (decision is Presentation.Decision.Silent) {
                if (decision.reason == NotShownReason.OLD) offline++
                db.messages().setPresented(m.serverRef, m.seq, MessageEntity.STATE_SILENT, decision.reason, m.notifId, 0)
                receipt(m, ReceiptState.NOT_SHOWN, decision.reason)
                continue
            }
            val wait = pacer.delayFor(now)
            if (wait > 0) delay(wait)
            val alert = m.kind == "problem" || m.kind == "test"
            if (m.kind == "recovery") db.messages().stopReminders(m.serverRef, m.sid)
            // Every post needs room, a recovery or an update too when its alarm is no longer on screen
            val roomSev = if (alert) m.sev else -1
            if (!notifier.makeRoom(roomSev, m.notifId)) {
                db.messages().setPresented(m.serverRef, m.seq, MessageEntity.STATE_SILENT, NotShownReason.CAP, m.notifId, 0)
                receipt(m, ReceiptState.NOT_SHOWN, NotShownReason.CAP)
                continue
            }
            var shown = notifier.post(m, s.label, chId, alert, 0, ch?.takeIf { it.kind == "custom" })
            if (!shown && notifier.makeRoom(roomSev, m.notifId)) {
                // Refused or late: once more, after making room again
                shown = notifier.post(m, s.label, chId, alert, 0, ch?.takeIf { it.kind == "custom" })
            }
            pacer.posted(System.currentTimeMillis())
            val next = if (shown && m.kind == "problem" && prefs.reminders != 0) now + prefs.reminderMinutes * 60_000L else 0
            db.messages().setPresented(m.serverRef, m.seq, MessageEntity.STATE_NOTIFIED, null, m.notifId, next)
            when {
                shown -> receipt(m, ReceiptState.SHOWN, null)
                notifier.atLimit() -> receipt(m, ReceiptState.NOT_SHOWN, NotShownReason.OS_LIMIT)
                else -> receipt(m, ReceiptState.NOT_SHOWN, NotShownReason.NOT_DISPLAYED)
            }
        }
        if (offline > 0) {
            notifier.system(Notifier.ID_OFFLINE_SUMMARY, text.getString(R.string.sys_offline_title),
                text.resources.getQuantityString(R.plurals.sys_offline_text, offline, offline))
        }
        Reminders.schedule(app)
    }

    private suspend fun receipt(m: MessageEntity, state: String, reason: String?) {
        db.receipts().insert(ReceiptEntity(serverRef = m.serverRef, msgId = m.msgId, seq = m.seq, state = state, reason = reason))
        conns[m.serverRef]?.let { flushReceipts(m.serverRef, it) }
    }

    /** Re-alerts overdue unresolved alarms */
    suspend fun runReminders() {
        val now = System.currentTimeMillis()
        if (settings.dndUntil() > now) return
        for (m in db.messages().dueReminders(now)) {
            val latest = db.messages().latest(m.serverRef, m.sid)
            val acked = latest?.body?.contains("\"acknowledged\":true") == true
            // A swiped-away notification keeps reminding: only open, Silence, ack or resolution stop it
            if (latest?.kind == "recovery" || acked) {
                db.messages().stopReminders(m.serverRef, m.sid)
                continue
            }
            val s = db.servers().get(m.serverRef) ?: continue
            val chId = m.channels.split(',').firstOrNull { it.isNotEmpty() } ?: "sev_${m.sev}"
            val ch = db.channels().get(s.id, chId)
            val prefs = ch?.let { ChannelPrefs(it.notify, it.reminders, it.reminderMinutes) } ?: ChannelPrefs.defaultsFor(m.sev)
            val done = m.reminders + 1
            // A reminder of a notification no longer on screen needs room like a new alarm
            if (notifier.makeRoom(m.sev, m.notifId)) notifier.post(m, s.label, chId, true, done, ch?.takeIf { it.kind == "custom" })
            db.messages().reminded(m.serverRef, m.seq, if (prefs.remindAgain(done)) now + prefs.reminderMinutes * 60_000L else 0)
        }
        Reminders.schedule(app)
    }

    // ---------------------------------------------------------------- Do Not Disturb

    suspend fun setDnd(untilMs: Long) {
        val now = System.currentTimeMillis()
        if (untilMs > now && settings.dndUntil() <= now) settings.setDndSince(now)
        settings.setDndUntil(untilMs)
        dndUntil = untilMs
        sendStatus()
        Reminders.schedule(app)
        DeliveryService.refresh(app)
    }

    /** End of Do Not Disturb: one summary of what arrived silently */
    suspend fun dndEnded() {
        val since = settings.dndSince()
        if (since <= 0) {
            settings.setDndUntil(0)
            dndUntil = 0
            DeliveryService.refresh(app)
            return
        }
        settings.setDndSince(0)
        settings.setDndUntil(0)
        dndUntil = 0
        val n = db.messages().silentSince(since)
        if (n > 0) notifier.system(Notifier.ID_DND_SUMMARY, text.getString(R.string.sys_dnd_title), text.resources.getQuantityString(R.plurals.sys_dnd_text, n, n))
        sendStatus()
        DeliveryService.refresh(app)
    }

    suspend fun sendStatus() {
        val dnd = settings.dndUntil().takeIf { it > System.currentTimeMillis() }
        val frame = Frames.status(deviceInfo(), Permissions.json(app), dnd)
        conns.values.forEach { it.send(frame) }
    }

    // ---------------------------------------------------------------- configuration and problems

    suspend fun refreshConfig(s: ServerEntity) {
        val cfg = api(s).config()
        if (cfg.serverId != s.serverId) return
        if (cfg.serviceUrls.isNotEmpty()) db.servers().setUrls(s.id, cfg.serviceUrls.joinToString("\n"))
        db.sources().replace(s.id, cfg.sources.map { SourceEntity(s.id, it.id, it.name, it.frontendUrl, it.apiMode, db.sources().get(s.id, it.id)?.stale ?: false) })
        val existing = db.channels().of(s.id).associateBy { it.id }
        val rows = cfg.channels.map { c ->
            val old = existing[c.id]
            val d = ChannelPrefs.defaultsFor(Notifier.severityOf(c.id) ?: 5)
            ChannelEntity(s.id, c.id, c.kind, c.name, c.enabled, old?.notify ?: true, old?.reminders ?: d.reminders, old?.reminderMinutes ?: d.reminderMinutes, c.color)
        }
        db.channels().upsert(rows)
        db.channels().prune(s.id, rows.map { it.id }.ifEmpty { listOf("") })
        notifier.syncChannels(s.id, s.label, rows)
        // The admin may have enabled or disabled the Zabbix API of a source: the Problems tab follows
        // without waiting for the next connection (features otherwise arrive with the welcome)
        val hasApi = cfg.sources.any { it.apiMode != "disabled" }
        // The same for the permission to close alerts, which follows the perimeter
        // Start from the stored features, not from s: a welcome may have updated them since s was read
        val current = db.servers().get(s.id)?.features ?: s.features
        val features = current.split(',').filter { it.isNotEmpty() && it != "problems" && it != "close" && it != "problem_views" } +
            (if (hasApi) listOf("problems") else emptyList()) + (if ("close" in cfg.features) listOf("close") else emptyList()) +
            (if (hasApi && "problem_views" in cfg.features) listOf("problem_views") else emptyList())
        db.servers().setWelcome(s.id, features.joinToString(","), cfg.nodeId)
        runCatching { Updater.offer(s.id, cfg.appUpdate) }.onFailure { warn("update offer", it) }
        if (hasApi && db.servers().get(s.id)?.projRev == 0L) runCatching { fetchSnapshot(s) }.onFailure { warn("snapshot", it) }
        if (!hasApi) {
            db.problems().deleteServer(s.id)
            db.servers().setProjRev(s.id, 0)
            lists.remove(s.id)
        }
    }

    private suspend fun rotateToken(s: ServerEntity) {
        val fresh = api(s).rotate()
        db.servers().setToken(s.id, Vault.seal(fresh))
    }

    private suspend fun list(ref: Long): ProblemList {
        lists[ref]?.let { return it }
        val l = ProblemList()
        val rows = db.problems().of(ref).mapNotNull { runCatching { WireJson.decodeFromString(ProblemRow.serializer(), it.json) }.getOrNull() }
        val rev = db.servers().get(ref)?.projRev ?: 0
        if (rev > 0) l.snapshot(rev, rows)
        return lists.putIfAbsent(ref, l) ?: l
    }

    suspend fun fetchSnapshot(s: ServerEntity) {
        // Servers with problem views also send the resolved problems of the last 7 days (Recent and History)
        val views = "problem_views" in (db.servers().get(s.id)?.features ?: s.features).split(',')
        val snap = api(s).snapshot(if (views) (LIST_KEEP_MS / 1000).toInt() else 0)
        val l = list(s.id)
        l.snapshot(snap.rev, snap.problems)
        db.problems().replace(s.id, snap.problems.map { it.entity(s.id) })
        db.servers().setProjRev(s.id, snap.rev)
        snap.sources.forEach { db.sources().setStale(s.id, it.id, it.stale) }
    }

    private suspend fun onDelta(s: ServerEntity, f: Incoming.Delta) {
        val l = list(s.id)
        if (!l.apply(f.delta)) {
            runCatching { fetchSnapshot(s) }.onFailure { warn("snapshot", it) }
            return
        }
        db.problems().upsert(f.delta.upsert.map { it.entity(s.id) })
        (f.delta.remove + l.prune(System.currentTimeMillis())).forEach { db.problems().delete(s.id, it.source, it.eventid) }
        db.servers().setProjRev(s.id, l.rev)
    }

    private fun ProblemRow.entity(ref: Long) =
        ProblemEntity(ref, source, eventid, WireJson.encodeToString(ProblemRow.serializer(), this), severity, clock, status)

    // ---------------------------------------------------------------- acks

    /** Queues an ack typed in the Detail; it is sent now or as soon as the server is reachable */
    suspend fun queueAck(ref: Long, source: String, eventid: String, text: String): String {
        val id = UUID.randomUUID().toString()
        val now = System.currentTimeMillis()
        db.acks().insert(AckEntity(id, ref, source, eventid, text, AckEntity.QUEUED, null, now, now))
        scope.launch { sendQueuedAcks(ref) }
        return id
    }

    private suspend fun sendQueuedAcks(ref: Long) {
        val s = db.servers().get(ref) ?: return
        for (a in db.acks().queued(ref)) {
            try {
                val st = api(s).ack(a.requestId, a.source, a.eventid, a.text)
                db.acks().setState(a.requestId, st.state, st.reason, System.currentTimeMillis())
            } catch (e: ApiException) {
                // Refused by the server (mode, perimeter, text): final, never retried
                db.acks().setState(a.requestId, AckEntity.REJECTED, e.code, System.currentTimeMillis())
            } catch (e: Exception) {
                return // offline: stays queued, sent on the next connection
            }
        }
        // Accepted acks whose ack.result was missed (disconnection, or it overtook the POST response)
        for (a in db.acks().accepted(ref)) {
            val st = runCatching { api(s).ackState(a.requestId) }.getOrNull() ?: return
            if (st.state != AckEntity.ACCEPTED) db.acks().setState(a.requestId, st.state, st.reason, System.currentTimeMillis())
        }
    }

    // ---------------------------------------------------------------- servers

    sealed interface EnrollResult {
        data class Ok(val serverRef: Long) : EnrollResult
        data class Failed(val reason: String) : EnrollResult
    }

    /** Adds a server; refuses one already present (same server_id) and the configured maximum */
    suspend fun enroll(url: String, code: String?, username: String?, password: String?, pins: List<String>, cleartextAccepted: Boolean): EnrollResult {
        val servers = db.servers().all()
        if (servers.size >= settings.maxServers()) return EnrollResult.Failed("max_servers")
        val link = Link(url.trim().trimEnd('/'), pins, cleartextAccepted)
        val api = Api(link, null)
        val res = try {
            if (code != null) api.enrollWithCode(code, deviceInfo()) else api.enrollWithPassword(username!!, password!!, deviceInfo())
        } catch (e: ApiException) {
            return EnrollResult.Failed(e.code)
        } catch (e: Exception) {
            return EnrollResult.Failed("network: ${e.message}")
        }
        if (servers.any { it.serverId == res.serverId }) {
            runCatching { Api(link, res.token).logout() } // revoke the token we just received
            return EnrollResult.Failed("already_added")
        }
        val label = runCatching { java.net.URI(link.baseUrl).host }.getOrNull() ?: link.baseUrl
        val ref = db.servers().insert(
            ServerEntity(
                label = label, baseUrl = link.baseUrl, urls = res.serviceUrls.joinToString("\n"), serverId = res.serverId,
                nodeId = res.nodeId, username = res.username, deviceId = res.deviceId, tokenEnc = Vault.seal(res.token),
                pins = pins.joinToString(","), cleartextAccepted = cleartextAccepted, ackedSeq = -1, createdAt = System.currentTimeMillis(),
            ),
        )
        settings.setQuit(false)
        reconcile()
        DeliveryService.start(app)
        return EnrollResult.Ok(ref)
    }

    /** Logout of one server: revoke the token, forget everything local */
    suspend fun logout(ref: Long) {
        val s = db.servers().get(ref) ?: return
        conns.remove(ref)?.let {
            it.send(Frames.bye("logout"))
            it.stop()
        }
        // Queued first, so that a server unreachable now still gets the revocation later
        Revocations.add(linksOf(s), s.tokenEnc)
        if (Revocations.retry()) Reminders.watchdog(app)
        trackers.remove(ref)
        lists.remove(ref)
        db.forgetServer(ref)
        notifier.deleteServerChannels(ref)
        if (db.servers().all().isEmpty()) DeliveryService.stop(app)
    }

    /** Logs a failure without its payload: class and message only (messages never carry tokens) */
    fun warn(what: String, t: Throwable) {
        android.util.Log.w("Zweep", "$what failed: ${t.javaClass.simpleName}: ${t.message?.take(200)}")
    }

    fun deviceInfo(): DeviceInfo {
        val name = runCatching { AndroidSettings.Global.getString(app.contentResolver, AndroidSettings.Global.DEVICE_NAME) }.getOrNull() ?: Build.MODEL
        return DeviceInfo(name.take(64), BuildConfig.VERSION_NAME, "Android ${Build.VERSION.RELEASE}", Build.MANUFACTURER, Build.MODEL, BuildConfig.VERSION_CODE.toLong())
    }

    suspend fun purgeHistory() {
        db.messages().purge(System.currentTimeMillis() - settings.historyMinutes() * 60_000L)
    }
}
