// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationChannelGroup
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.media.AudioAttributes
import android.net.Uri
import android.provider.Settings as AndroidSettings
import net.nicodroid.zweep.R
import net.nicodroid.zweep.core.Rotation
import net.nicodroid.zweep.core.Severity
import net.nicodroid.zweep.core.Shown
import net.nicodroid.zweep.data.ChannelEntity
import net.nicodroid.zweep.data.MessageEntity
import net.nicodroid.zweep.ui.MainActivity

/** Android notifications of Zweep: channels, alarm posts, rotation within the cap, summaries */
class Notifier(private val ctx: Context) {
    private val nm = ctx.getSystemService(NotificationManager::class.java)

    fun ensureBaseChannels() {
        nm.createNotificationChannel(
            NotificationChannel(CH_SERVICE, ctx.getString(R.string.channel_service), NotificationManager.IMPORTANCE_MIN).apply {
                setShowBadge(false)
            },
        )
        nm.createNotificationChannel(
            NotificationChannel(CH_SYSTEM, ctx.getString(R.string.channel_system), NotificationManager.IMPORTANCE_DEFAULT),
        )
    }

    /**
     * One Android channel per Zweep channel of the server (6 severities, the custom ones, the
     * recoveries). Each plays a Zweep sound (res/raw: zweep_sev_N, zweep_custom, zweep_resolved), which
     * every user can change in the Android settings of the channel. Android fixes the sound of a channel
     * when it is created and restores the old settings if a deleted id is created again, so a channel
     * lives in "generations": zw.<server>.<channel>.s<G>. The channels made before their Zweep sound
     * (generation 1, no suffix) move to .s2 only while they still use the system sound; a sound the user
     * chose (or turned off) is kept. [restoreZweepSound] creates the next generation.
     */
    fun syncChannels(serverRef: Long, label: String, channels: List<ChannelEntity>) {
        val group = "zw.$serverRef"
        nm.createNotificationChannelGroup(NotificationChannelGroup(group, label))
        val keep = mutableSetOf<String>()
        // Recoveries ring on their own channel, with the Zweep "resolved" sound
        val all = channels.filter { it.id != RESOLVED } + ChannelEntity(serverRef, RESOLVED, "resolved", RESOLVED)
        for (c in all) {
            val sev = severityOf(c.id)
            val gen = generation(serverRef, c.id)
            val current = generationId(serverRef, c.id, gen)
            val old = nm.getNotificationChannel(current)
            when {
                old == null -> createChannel(generationId(serverRef, c.id, 2), group, c, sev).also { keep += it }
                gen == 1 && !userChoseSound(old) -> {
                    nm.deleteNotificationChannel(current)
                    keep += createChannel(generationId(serverRef, c.id, 2), group, c, sev)
                }
                else -> {
                    // Existing channel: Android keeps the user's settings, only the name and group follow
                    keep += current
                    nm.createNotificationChannel(NotificationChannel(current, channelName(c), old.importance).apply { this.group = group })
                }
            }
        }
        nm.notificationChannels.filter { it.group == group && it.id !in keep }.forEach { nm.deleteNotificationChannel(it.id) }
    }

    private fun createChannel(id: String, group: String, c: ChannelEntity, sev: Int?): String {
        val importance = when {
            c.id == RESOLVED -> NotificationManager.IMPORTANCE_DEFAULT
            sev == null -> NotificationManager.IMPORTANCE_HIGH
            sev >= Severity.HIGH -> NotificationManager.IMPORTANCE_HIGH
            else -> NotificationManager.IMPORTANCE_DEFAULT // the softest sounds too: they must be heard
        }
        val ch = NotificationChannel(id, channelName(c), importance).apply {
            this.group = group
            lockscreenVisibility = Notification.VISIBILITY_PRIVATE
            enableVibration(true)
            if (sev != null) {
                enableLights(true)
                lightColor = severityColor(sev)
            }
            setSound(zweepSound(c.id), soundAttributes)
        }
        nm.createNotificationChannel(ch)
        return id
    }

    private fun syncChannelsAdd(serverRef: Long, label: String, c: ChannelEntity) {
        val existing = nm.notificationChannels.filter { it.group == "zw.$serverRef" }
            .map { it.id.substringAfter("zw.$serverRef.").replace(GENERATION, "") }
        syncChannels(serverRef, label, (existing.map { ChannelEntity(serverRef, it, "severity", it) } + c).distinctBy { it.id })
    }

    /** Highest generation of a channel present on the phone (0: none) */
    private fun generation(serverRef: Long, channelId: String): Int {
        val base = androidChannel(serverRef, channelId)
        return nm.notificationChannels.mapNotNull {
            when {
                it.id == base -> 1
                it.id.startsWith("$base.s") -> it.id.removePrefix("$base.s").toIntOrNull()
                else -> null
            }
        }.maxOrNull() ?: 0
    }

    private fun generationId(serverRef: Long, channelId: String, gen: Int) =
        if (gen <= 1) androidChannel(serverRef, channelId) else androidChannel(serverRef, channelId) + ".s$gen"

    /** The Android channel that carries a Zweep channel now */
    fun channelFor(serverRef: Long, channelId: String): String {
        val gen = generation(serverRef, channelId)
        return generationId(serverRef, channelId, if (gen == 0) 2 else gen)
    }

    /** Whether a channel still plays its Zweep sound */
    fun usesZweepSound(serverRef: Long, channelId: String): Boolean {
        val sound = zweepSound(channelId)
        val ch = nm.getNotificationChannel(channelFor(serverRef, channelId)) ?: return true
        return ch.sound == sound
    }

    /**
     * Gives a channel its Zweep sound back: Android cannot change the sound of a channel, so the
     * next generation is created (the other settings of the channel go back to their defaults)
     */
    fun restoreZweepSound(serverRef: Long, c: ChannelEntity) {
        val sev = severityOf(c.id)
        val gen = generation(serverRef, c.id)
        if (gen > 0) nm.deleteNotificationChannel(generationId(serverRef, c.id, gen))
        createChannel(generationId(serverRef, c.id, maxOf(gen, 1) + 1), "zw.$serverRef", c, sev)
    }

    /** A sound other than the system default (or no sound): chosen by the user, never overwritten */
    private fun userChoseSound(ch: NotificationChannel): Boolean =
        ch.sound == null || ch.sound != AndroidSettings.System.DEFAULT_NOTIFICATION_URI

    /**
     * Every Zweep channel has a Zweep sound: its severity, the recoveries, or the one sound of the custom
     * channels (each can be changed in the Android settings of its channel)
     */
    private fun zweepSound(channelId: String): Uri = when {
        channelId == RESOLVED -> raw(R.raw.zweep_resolved)
        else -> severityOf(channelId)?.let { severitySound(it) } ?: raw(R.raw.zweep_custom)
    }

    private fun raw(res: Int): Uri = Uri.parse("android.resource://${ctx.packageName}/$res")

    private fun severitySound(sev: Int): Uri {
        val res = when (sev) {
            5 -> R.raw.zweep_sev_5
            4 -> R.raw.zweep_sev_4
            3 -> R.raw.zweep_sev_3
            2 -> R.raw.zweep_sev_2
            1 -> R.raw.zweep_sev_1
            else -> R.raw.zweep_sev_0
        }
        return Uri.parse("android.resource://${ctx.packageName}/$res")
    }

    private val soundAttributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_NOTIFICATION)
        .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
        .build()

    fun deleteServerChannels(serverRef: Long) {
        val group = "zw.$serverRef"
        nm.notificationChannels.filter { it.group == group }.forEach { nm.deleteNotificationChannel(it.id) }
        nm.deleteNotificationChannelGroup(group)
    }

    fun channelBlocked(serverRef: Long, channelId: String): Boolean {
        val ch = nm.getNotificationChannel(channelFor(serverRef, channelId)) ?: return false
        return ch.importance == NotificationManager.IMPORTANCE_NONE
    }

    fun notificationsEnabled() = nm.areNotificationsEnabled()

    /** Alarm notifications currently shown, for the cap rotation */
    fun shown(): List<Shown> = nm.activeNotifications.filter { it.tag == TAG_ALARM }.map {
        Shown(it.id.toString(), it.notification.extras.getInt(EXTRA_SEV), it.notification.extras.getBoolean(EXTRA_RESOLVED), it.postTime)
    }

    /**
     * Makes room for the notification [id] of an alarm of [severity] (-1: a recovery or update, which
     * only replaces resolved alarms). Counts every notification of the app against the safe total.
     * False when every shown alarm is unresolved and more severe.
     */
    fun makeRoom(severity: Int, id: Int): Boolean {
        val active = nm.activeNotifications
        if (active.any { it.tag == TAG_ALARM && it.id == id }) return true // updated in place
        val alarms = active.filter { it.tag == TAG_ALARM }.map {
            Shown(it.id.toString(), it.notification.extras.getInt(EXTRA_SEV), it.notification.extras.getBoolean(EXTRA_RESOLVED), it.postTime)
        }
        val victims = Rotation.victims(alarms, severity, Rotation.alarmCap(active.size - alarms.size)) ?: return false
        victims.forEach { nm.cancel(TAG_ALARM, it.key.toInt()) }
        return true
    }

    /** The app is at the safe total: Android may refuse more */
    fun atLimit() = nm.activeNotifications.size >= Rotation.SAFE_TOTAL

    /**
     * Posts (or updates) the notification of an alarm. [alert] false updates silently (recovery,
     * repeated post of the same event). Returns true if the notification is active afterwards.
     */
    fun post(m: MessageEntity, serverLabel: String, channelId: String, alert: Boolean, reminder: Int, custom: ChannelEntity? = null): Boolean {
        if (nm.getNotificationChannel(channelFor(m.serverRef, channelId)) == null) {
            // Configuration not loaded yet: a channel for the message is created rather than losing the alarm
            syncChannelsAdd(m.serverRef, serverLabel, ChannelEntity(m.serverRef, channelId, if (channelId.startsWith("sev_")) "severity" else "custom", channelId))
        }
        val resolved = m.kind == "recovery"
        val sevLabel = ctx.getString(severityLabel(m.sev))
        // A custom channel takes the place of the severity: a container with its own name and color
        val accent = custom?.color?.let { parseColor(it) } ?: custom?.let { BRAND } ?: severityColor(m.sev)
        // In a custom channel the notification shows the channel; the severity is in the detail of the app
        val label = custom?.name ?: sevLabel
        val status = when (m.kind) {
            "recovery" -> ctx.getString(R.string.state_resolved)
            "update" -> ctx.getString(R.string.state_updated)
            "test" -> ctx.getString(R.string.state_test)
            else -> ctx.getString(R.string.state_open) // problem, and a repeat of Zabbix
        }
        val open = PendingIntent.getActivity(
            ctx, m.notifId, Intent(ctx, MainActivity::class.java).setAction(MainActivity.ACTION_OPEN)
                .putExtra(MainActivity.EXTRA_SERVER, m.serverRef).putExtra(MainActivity.EXTRA_SID, m.sid)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val silence = PendingIntent.getBroadcast(
            ctx, m.notifId, Intent(ctx, ActionReceiver::class.java).setAction(ActionReceiver.SILENCE)
                .putExtra(MainActivity.EXTRA_SERVER, m.serverRef).putExtra(MainActivity.EXTRA_SID, m.sid),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        // Lock screen before unlock and on shared screens: only the severity
        val androidId = channelFor(m.serverRef, channelId)
        val public = Notification.Builder(ctx, androidId)
            .setSmallIcon(R.drawable.ic_stat_zweep)
            .setContentTitle(ctx.getString(R.string.notification_public, label))
            .setColor(accent)
            .build()
        val sub = listOf(m.sourceName.ifEmpty { m.source }, serverLabel).filter { it.isNotEmpty() }.distinct().joinToString(" · ")
        val b = Notification.Builder(ctx, androidId)
            .setSmallIcon(R.drawable.ic_stat_zweep)
            .setContentTitle(m.title)
            .setContentText("$status · $sub")
            .setSubText(label)
            .setColor(accent)
            .setWhen(if (m.eventTime > 0) m.eventTime else m.receivedAt)
            .setShowWhen(true)
            .setCategory(if (m.sev >= Severity.HIGH) Notification.CATEGORY_ALARM else Notification.CATEGORY_EVENT)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setPublicVersion(public)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setOnlyAlertOnce(!alert)
            .setGroup("zw.alarms")
            .setSortKey("%d%013d".format(9 - m.sev.coerceAtLeast(0), Long.MAX_VALUE / 1000 - m.receivedAt / 1000))
        if (reminder > 0) b.setSubText("$label · " + ctx.getString(R.string.notification_reminder, reminder))
        // Announcements from the dashboard (e.g. a planned maintenance) carry a text: shown in full
        bodyText(m.body)?.let { b.setStyle(Notification.BigTextStyle().bigText("$it\n$status · $sub")) }
        if (!resolved && m.kind != "test") {
            b.addAction(Notification.Action.Builder(null, ctx.getString(R.string.action_silence), silence).build())
        }
        val extras = b.extras
        extras.putInt(EXTRA_SEV, m.sev)
        extras.putBoolean(EXTRA_RESOLVED, resolved)
        nm.notify(TAG_ALARM, m.notifId, b.build())
        // notify() is asynchronous: the notification shows up among the active ones shortly after
        // (up to a couple of seconds on a busy or sleeping phone)
        repeat(20) {
            if (isActive(m.notifId)) return true
            Thread.sleep(100)
        }
        return false
    }

    fun isActive(id: Int) = nm.activeNotifications.any { it.tag == TAG_ALARM && it.id == id }

    fun cancel(id: Int) = nm.cancel(TAG_ALARM, id)

    /** System notices: gaps, revoked tokens, rejected acks, summaries */
    fun system(id: Int, title: String, text: String) {
        val open = PendingIntent.getActivity(
            ctx, id, Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE,
        )
        nm.notify(
            TAG_SYSTEM, id,
            Notification.Builder(ctx, CH_SYSTEM).setSmallIcon(R.drawable.ic_stat_zweep).setContentTitle(title)
                .setContentText(text).setStyle(Notification.BigTextStyle().bigText(text)).setContentIntent(open)
                .setAutoCancel(true).setColor(BRAND).build(),
        )
    }

    private fun channelName(c: ChannelEntity): String = when (c.id) {
        RESOLVED -> ctx.getString(R.string.channel_resolved)
        else -> severityOf(c.id)?.let { ctx.getString(severityLabel(it)) } ?: c.name
    }

    companion object {
        const val CH_SERVICE = "zw.service"
        const val CH_SYSTEM = "zw.system"
        /** Channel of the recoveries of a server (zw.<server>.resolved.s<G>), with the Zweep resolved sound */
        const val RESOLVED = "resolved"
        const val TAG_ALARM = "alarm"
        const val TAG_SYSTEM = "system"
        const val EXTRA_SEV = "zw.sev"
        const val EXTRA_RESOLVED = "zw.resolved"
        const val ID_SERVICE = 1
        const val ID_OFFLINE_SUMMARY = 2
        const val ID_DND_SUMMARY = 3
        const val ID_GAP = 4
        const val ID_REVOKED = 5
        const val ID_ACK = 6
        const val ID_PERMISSIONS = 7
        /** Revocation notices use one id per server from here on */
        const val ID_REVOKED_BASE = 1000
        val BRAND = Color.parseColor("#D32F2E")

        /** #RRGGBB of the dashboard palette to an ARGB color, or null */
        fun parseColor(hex: String): Int? =
            hex.takeIf { it.length == 7 && it[0] == '#' }?.substring(1)?.toLongOrNull(16)?.let { (0xFF000000 or it).toInt() }

        fun androidChannel(serverRef: Long, channelId: String) = "zw.$serverRef.$channelId"

        /** Generation suffix of the severity channels (".s2", ".s3", ...) */
        private val GENERATION = Regex("""\.s\d+$""")

        fun severityOf(channelId: String): Int? =
            channelId.removePrefix("sev_").takeIf { channelId.startsWith("sev_") }?.toIntOrNull()?.takeIf { it in Severity.all }

        /** Stable id of the notification of one event of one server (updates replace it) */
        fun notifId(serverRef: Long, sid: String): Int {
            val h = (serverRef.toString() + "/" + sid).hashCode() and 0x3fffffff
            return h + 100 // below 100: system notifications
        }

        fun severityLabel(sev: Int) = when (sev.coerceAtLeast(0)) {
            0 -> R.string.sev_0
            1 -> R.string.sev_1
            2 -> R.string.sev_2
            3 -> R.string.sev_3
            4 -> R.string.sev_4
            else -> R.string.sev_5
        }

        fun severityColor(sev: Int) = Color.parseColor(
            when (sev.coerceAtLeast(0)) {
                0 -> "#97AAB3"
                1 -> "#7499FF"
                2 -> "#FFC859"
                3 -> "#FFA059"
                4 -> "#E97659"
                else -> "#E45959"
            },
        )
    }
}

/** The free text of a message written from the dashboard ("message" in the body), if any */
fun bodyText(body: String): String? = runCatching {
    (net.nicodroid.zweep.core.WireJson.parseToJsonElement(body) as kotlinx.serialization.json.JsonObject)["message"]
        ?.let { (it as kotlinx.serialization.json.JsonPrimitive).content }?.takeIf { it.isNotBlank() }
}.getOrNull()
