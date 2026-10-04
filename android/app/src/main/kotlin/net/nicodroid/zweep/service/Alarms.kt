// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlinx.coroutines.launch
import net.nicodroid.zweep.ui.MainActivity

/** Timed wake-ups: reminders, end of Do Not Disturb, and the 15-minute watchdog */
object Reminders {
    private const val WATCHDOG_MS = 15 * 60_000L

    fun schedule(ctx: Context) {
        Engine.scope.launch {
            Engine.db.messages().nextReminder()?.let { set(ctx, AlarmReceiver.REMINDER, it) } ?: cancel(ctx, AlarmReceiver.REMINDER)
            val dnd = Engine.settings.dndUntil()
            if (dnd > System.currentTimeMillis()) set(ctx, AlarmReceiver.DND_END, dnd) else cancel(ctx, AlarmReceiver.DND_END)
        }
    }

    fun watchdog(ctx: Context) = set(ctx, AlarmReceiver.WATCHDOG, System.currentTimeMillis() + WATCHDOG_MS)

    fun cancelAll(ctx: Context) = listOf(AlarmReceiver.REMINDER, AlarmReceiver.DND_END, AlarmReceiver.WATCHDOG).forEach { cancel(ctx, it) }

    private fun intent(ctx: Context, action: String) = PendingIntent.getBroadcast(
        ctx, action.hashCode(), Intent(ctx, AlarmReceiver::class.java).setAction(action), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun set(ctx: Context, action: String, at: Long) {
        val am = ctx.getSystemService(AlarmManager::class.java)
        val pi = intent(ctx, action)
        // Exact while idle when allowed (USE_EXACT_ALARM, not distributed via Play); otherwise inexact
        if (Permissions.exactAlarms(ctx)) am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at, pi)
        else am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at, pi)
    }

    private fun cancel(ctx: Context, action: String) = ctx.getSystemService(AlarmManager::class.java).cancel(intent(ctx, action))
}

class AlarmReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        Engine.init(ctx)
        val pending = goAsync()
        Engine.scope.launch {
            try {
                when (intent.action) {
                    WATCHDOG -> {
                        val revoking = Revocations.retry()
                        if (Engine.db.servers().all().isNotEmpty() && !Engine.settings.quit()) {
                            DeliveryService.start(ctx)
                            Engine.pingAll()
                            Engine.purgeHistory()
                            Reminders.watchdog(ctx)
                        } else if (revoking) {
                            Reminders.watchdog(ctx)
                        }
                    }
                    REMINDER -> Engine.runReminders()
                    DND_END -> Engine.dndEnded()
                }
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        const val WATCHDOG = "net.nicodroid.zweep.WATCHDOG"
        const val REMINDER = "net.nicodroid.zweep.REMINDER"
        const val DND_END = "net.nicodroid.zweep.DND_END"
    }
}

/** Starts delivery after a reboot (also before the first unlock) and after an update */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action !in setOf(Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_LOCKED_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED)) return
        Engine.init(ctx)
        val pending = goAsync()
        Engine.scope.launch {
            try {
                if (Engine.db.servers().all().isNotEmpty() && !Engine.settings.quit()) DeliveryService.start(ctx)
            } finally {
                pending.finish()
            }
        }
    }
}

/** Actions of notifications: silence reminders, Do Not Disturb from the service notification */
class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        Engine.init(ctx)
        val pending = goAsync()
        Engine.scope.launch {
            try {
                val now = System.currentTimeMillis()
                when (intent.action) {
                    SILENCE -> Engine.db.messages().silence(intent.getLongExtra(MainActivity.EXTRA_SERVER, 0), intent.getStringExtra(MainActivity.EXTRA_SID) ?: "")
                    DND -> {
                        val minutes = intent.getIntExtra(EXTRA_MINUTES, 0)
                        Engine.setDnd(if (minutes > 0) now + minutes * 60_000L else untilEight(now))
                    }
                    DND_OFF -> Engine.dndEnded()
                }
                Reminders.schedule(ctx)
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        const val SILENCE = "net.nicodroid.zweep.SILENCE"
        const val DND = "net.nicodroid.zweep.DND"
        const val DND_OFF = "net.nicodroid.zweep.DND_OFF"
        const val EXTRA_MINUTES = "minutes"

        /** Next 08:00 local time */
        fun untilEight(now: Long): Long {
            val c = java.util.Calendar.getInstance().apply {
                timeInMillis = now
                set(java.util.Calendar.HOUR_OF_DAY, 8)
                set(java.util.Calendar.MINUTE, 0)
                set(java.util.Calendar.SECOND, 0)
                set(java.util.Calendar.MILLISECOND, 0)
            }
            if (c.timeInMillis <= now) c.add(java.util.Calendar.DAY_OF_MONTH, 1)
            return c.timeInMillis
        }
    }
}
