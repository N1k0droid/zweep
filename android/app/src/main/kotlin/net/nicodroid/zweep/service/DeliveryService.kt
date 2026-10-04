// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.os.IBinder
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import net.nicodroid.zweep.R
import net.nicodroid.zweep.ui.MainActivity
import java.text.DateFormat
import java.util.Date

/**
 * Foreground service (specialUse) holding one WebSocket per server. Keepalive pings of all
 * servers are sent together (one radio wake-up); the server pings on its own only when the app is
 * silent (Doze), so the connection survives either way.
 */
class DeliveryService : Service() {
    private var pinger: Job? = null
    private var callback: ConnectivityManager.NetworkCallback? = null

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(net.nicodroid.zweep.ui.Lang.wrap(newBase))
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        Engine.init(this)
        running = this
        startForeground(Notifier.ID_SERVICE, build(), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        val cm = getSystemService(ConnectivityManager::class.java)
        callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = Engine.networkAvailable()
        }.also { cm.registerDefaultNetworkCallback(it) }
        Engine.scope.launch {
            Engine.reconcile()
            Engine.purgeHistory()
        }
        pinger = Engine.scope.launch {
            while (isActive) {
                delay(KEEPALIVE_MS)
                Engine.pingAll()
            }
        }
        Reminders.watchdog(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Engine.scope.launch { Engine.reconcile() }
        return START_STICKY
    }

    override fun onDestroy() {
        pinger?.cancel()
        callback?.let { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) }
        if (running === this) running = null
        super.onDestroy()
    }

    private fun build(): Notification {
        val connected = Engine.connectedCount()
        val total = Engine.serverCount()
        val dnd = Engine.dndUntil
        val now = System.currentTimeMillis()
        val title = when {
            dnd > now -> getString(R.string.service_dnd_until, DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(dnd)))
            total == 0 -> getString(R.string.service_starting)
            else -> getString(R.string.service_connected, connected, total)
        }
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val b = Notification.Builder(this, Notifier.CH_SERVICE)
            .setSmallIcon(R.drawable.ic_stat_zweep)
            .setContentTitle(title)
            .setContentText(getString(R.string.service_text))
            .setContentIntent(open)
            .setOngoing(true)
            .setColor(Notifier.BRAND)
            .setVisibility(Notification.VISIBILITY_PUBLIC)
        if (dnd > now) {
            b.addAction(action(ActionReceiver.DND_OFF, 0, getString(R.string.dnd_off)))
        } else {
            b.addAction(action(ActionReceiver.DND, 30, getString(R.string.dnd_30m)))
            b.addAction(action(ActionReceiver.DND, 60, getString(R.string.dnd_1h)))
            b.addAction(action(ActionReceiver.DND, -1, getString(R.string.dnd_until_8)))
        }
        return b.build()
    }

    private fun action(act: String, minutes: Int, label: String): Notification.Action {
        val pi = PendingIntent.getBroadcast(
            this, act.hashCode() + minutes, Intent(this, ActionReceiver::class.java).setAction(act).putExtra(ActionReceiver.EXTRA_MINUTES, minutes),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Action.Builder(null, label, pi).build()
    }

    companion object {
        private const val KEEPALIVE_MS = 60_000L

        @Volatile private var running: DeliveryService? = null

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, DeliveryService::class.java))
        }

        fun stop(ctx: Context) {
            Engine.stopAll()
            Reminders.cancelAll(ctx)
            ctx.stopService(Intent(ctx, DeliveryService::class.java))
        }

        /** Updates the persistent notification (connections, Do Not Disturb) */
        fun refresh(ctx: Context) {
            val s = running ?: return
            ctx.getSystemService(NotificationManager::class.java).notify(Notifier.ID_SERVICE, s.build())
        }
    }
}
